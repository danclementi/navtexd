package sitor

import (
	"fmt"
	"strings"
	"testing"

	"navtex/internal/fsk"
	"navtex/internal/navtex"
	"navtex/internal/testvec"
)

type refMsg struct {
	Station string `json:"station"`
	Subject string `json:"subject"`
	Serial  string `json:"serial"`
	Body    string `json:"body"`
}

type meta struct {
	Truth    map[string]string `json:"truth"`
	Channels map[string]struct {
		Messages []refMsg `json:"messages"`
		BitOrder string   `json:"bit_order"`
		Polarity int      `json:"polarity"`
	} `json:"channels"`
}

func lev(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func norm(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r", "")) }

// decodeAll feeds symbols in chunks and returns messages, all events, and final decoder.
func decodeAll(syms []fsk.Symbol, chunk int, cfg Config) ([]navtex.Message, []Event, *Decoder) {
	d := NewDecoder(cfg)
	var p navtex.Parser
	var msgs []navtex.Message
	var evs []Event
	handle := func(es []Event) {
		for _, e := range es {
			evs = append(evs, e)
			if e.Kind == EvText {
				msgs = append(msgs, p.Feed(e.Text)...)
			}
		}
	}
	for i := 0; i < len(syms); i += chunk {
		handle(d.Process(syms[i:min(i+chunk, len(syms))]))
	}
	handle(d.Flush())
	return msgs, evs, d
}

func refSymbols(t *testing.T, name, ch string) []fsk.Symbol {
	soft := testvec.F32(t, fmt.Sprintf("%s_%s_soft.f32", name, ch[:3]))
	lock := testvec.Bytes(t, fmt.Sprintf("%s_%s_lock.u8", name, ch[:3]))
	out := make([]fsk.Symbol, len(soft))
	for i := range soft {
		out[i] = fsk.Symbol{Soft: float64(soft[i]), Locked: lock[i] != 0}
	}
	return out
}

func truthBody(t *testing.T, m meta, ch string) navtex.Message {
	var p navtex.Parser
	ms := p.Feed(m.Truth[ch])
	if len(ms) != 1 {
		t.Fatalf("truth for %s does not parse: %v", ch, ms)
	}
	return ms[0]
}

var allCases = []string{"clean", "nominal", "lowmark", "msb", "impulsive", "weak", "usb_real"}

// Level 5: the reference's own soft symbols must decode as well as the reference did.
func TestDecodeReferenceSoftSymbols(t *testing.T) {
	for _, name := range allCases {
		var m meta
		testvec.JSON(t, name+".json", &m)
		for ch, info := range m.Channels {
			t.Run(name+"_"+ch, func(t *testing.T) {
				msgs, evs, d := decodeAll(refSymbols(t, name, ch), 1000, DefaultConfig())
				truth := truthBody(t, m, ch)
				refErr := 1 << 20
				if len(info.Messages) > 0 {
					refErr = lev(norm(info.Messages[0].Body), norm(truth.Body))
				}
				if len(msgs) == 0 {
					if len(info.Messages) == 0 {
						t.Skip("reference decoded nothing either")
					}
					t.Fatalf("no message decoded (reference: %d wrong chars); events: %v", refErr, evs)
				}
				got := msgs[0]
				gotErr := lev(norm(got.Body), norm(truth.Body))
				t.Logf("id %s (truth %s) order %v, wrong chars %d (reference %d)", got.ID(), truth.ID(), d.Order(), gotErr, refErr)
				if refErr == 0 && (gotErr != 0 || got.ID() != truth.ID()) {
					t.Errorf("reference is exact but got %q %q", got.ID(), got.Body)
				}
				if gotErr > refErr+2 {
					t.Errorf("%d wrong chars, reference %d", gotErr, refErr)
				}
			})
		}
	}
}

// Feeding symbols one at a time or all at once must give identical events.
func TestDecoderChunkInvariance(t *testing.T) {
	syms := refSymbols(t, "nominal", "518000")
	_, ref, _ := decodeAll(syms, len(syms), DefaultConfig())
	for _, c := range []int{1, 7, 999} {
		_, got, _ := decodeAll(syms, c, DefaultConfig())
		if fmt.Sprint(got) != fmt.Sprint(ref) {
			t.Fatalf("chunk %d: events differ", c)
		}
	}
}

// A fixed bit order must work without ZCZC detection, and the wrong one must not produce the message.
func TestFixedOrder(t *testing.T) {
	syms := refSymbols(t, "msb", "518000")
	cfg := DefaultConfig()
	cfg.Order = MSB
	if msgs, _, _ := decodeAll(syms, 500, cfg); len(msgs) != 1 {
		t.Errorf("msb fixed: %d messages", len(msgs))
	}
	cfg.Order = LSB
	if msgs, _, _ := decodeAll(syms, 500, cfg); len(msgs) != 0 {
		t.Errorf("lsb fixed on msb signal decoded %d messages", len(msgs))
	}
}
