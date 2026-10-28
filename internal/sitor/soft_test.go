package sitor

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"navtex/internal/fsk"
	"navtex/internal/navtex"
)

func softCfg() Config {
	c := DefaultConfig()
	c.Framing = FramingSoft
	return c
}

// decodeFramer feeds symbols in chunks to the decoder selected by cfg and parses the text into messages.
func decodeFramer(syms []fsk.Symbol, chunk int, cfg Config) ([]navtex.Message, []Event, string) {
	d := New(cfg)
	var p navtex.Parser
	var msgs []navtex.Message
	var evs []Event
	var text strings.Builder
	handle := func(es []Event) {
		for _, e := range es {
			evs = append(evs, e)
			if e.Kind == EvText {
				text.WriteString(e.Text)
				msgs = append(msgs, p.Feed(e.Text)...)
			}
		}
	}
	for i := 0; i < len(syms); i += chunk {
		handle(d.Process(syms[i:min(i+chunk, len(syms))]))
	}
	handle(d.Flush())
	return msgs, evs, text.String()
}

// codeOf finds the pattern (bit t = t-th transmitted bit) of a character in the given shift.
func codeOf(s string, fig bool) (uint8, bool) {
	tab := LTRS
	if fig {
		tab = FIGS
	}
	for m, c := range tab {
		if c == s {
			return m, true
		}
	}
	return 0, false
}

// encode turns text into a SITOR-B slot stream: idle phasing first, each character at an even slot and again five slots
// later (as a repeat in an odd slot), then idle again.
func encode(text string, idleSlots int) []uint8 {
	var chars []uint8
	fig := false
	for _, r := range text {
		s := string(r)
		if _, ok := codeOf(s, fig); !ok {
			sw := tokFIGS
			if fig {
				sw = tokLTRS
			}
			m, _ := codeOf(sw, fig)
			chars = append(chars, m)
			fig = !fig
		}
		m, ok := codeOf(s, fig)
		if !ok {
			panic("no code for " + s)
		}
		chars = append(chars, m)
	}
	alpha, rep := uint8(0x0F), uint8(0x66) // the two phasing signals
	var slots []uint8
	for i := 0; i < idleSlots; i++ {
		if i%2 == 0 {
			slots = append(slots, alpha)
		} else {
			slots = append(slots, rep)
		}
	}
	n := len(slots)
	for i := 0; i < 2*len(chars)+5+idleSlots; i++ {
		var m uint8
		switch {
		case i%2 == 0 && i/2 < len(chars):
			m = chars[i/2]
		case i%2 == 1 && i >= 5 && (i-5)/2 < len(chars):
			m = chars[(i-5)/2]
		case i%2 == 0:
			m = alpha
		default:
			m = rep
		}
		slots = append(slots, m)
	}
	_ = n
	return slots
}

// symbolsOf turns slot patterns into soft symbols (+-85 Hz, plus Gaussian noise of sd sigma); sign -1 inverts the polarity.
func symbolsOf(slots []uint8, sigma float64, sign float64, seed int64) []fsk.Symbol {
	rng := rand.New(rand.NewSource(seed))
	var out []fsk.Symbol
	for _, m := range slots {
		for t := 0; t < 7; t++ {
			v := -85.0
			if m>>t&1 == 1 {
				v = 85
			}
			out = append(out, fsk.Symbol{Soft: sign*v + sigma*rng.NormFloat64(), Locked: true})
		}
	}
	return out
}

const testText = "ZCZC KA91\r\nAVURNAV CHERBOURG 91/26\r\n\r\nWIGHT - DST BOULOGNE\r\nAIDS TO NAVIGATION CHANGES\r\nLIGHT UNRELIABLE\r\nVERGOYER SW BUOY BOULOGNE TSS\r\nMARK: WEST CARDINAL\r\n50-27.00N 001-00.00E\r\nNAVIGATE WITH CAUTION\r\nNNNN\r\n"

func TestSoftDecodesCleanSignal(t *testing.T) {
	syms := symbolsOf(encode(testText, 160), 0, 1, 1)
	msgs, evs, _ := decodeFramer(syms, 1000, softCfg())
	if len(msgs) != 1 || msgs[0].ID() != "KA91" || !strings.Contains(msgs[0].Body, "NAVIGATE WITH CAUTION") {
		t.Fatalf("messages %+v; events %v", msgs, evs[:min(len(evs), 8)])
	}
	var kinds []EventKind
	for _, e := range evs {
		if e.Kind != EvText {
			kinds = append(kinds, e.Kind)
		}
	}
	if len(kinds) < 2 || kinds[0] != EvOrder || kinds[1] != EvLock || kinds[len(kinds)-1] != EvUnlock {
		t.Errorf("events %v: want order, lock, ..., unlock", kinds)
	}
}

// Inverted polarity is found by itself (auto) and refused when the other polarity is forced.
func TestSoftPolarity(t *testing.T) {
	syms := symbolsOf(encode(testText, 160), 20, -1, 2)
	msgs, _, _ := decodeFramer(syms, 500, softCfg())
	if len(msgs) != 1 || msgs[0].ID() != "KA91" {
		t.Fatalf("auto polarity on an inverted signal: %+v", msgs)
	}
	c := softCfg()
	c.Polarity = PolNormal
	if msgs, _, _ := decodeFramer(syms, 500, c); len(msgs) != 0 {
		t.Errorf("forced normal polarity decoded an inverted signal: %+v", msgs)
	}
	c.Polarity = PolInverted
	if msgs, _, _ := decodeFramer(syms, 500, c); len(msgs) != 1 {
		t.Errorf("forced inverted polarity: %d messages", len(msgs))
	}
}

// In noise (55 Hz sd on +-85 Hz symbols) the soft lock gets the message through where the hard framing, which needs ten
// perfect words in a row, mostly cannot.
func TestSoftBeatsHardInNoise(t *testing.T) {
	var hardOK, softOK int
	for seed := int64(1); seed <= 12; seed++ {
		syms := symbolsOf(encode(testText, 160), 55, 1, seed)
		hm, _, _ := decodeFramer(syms, 1000, DefaultConfig())
		sm, _, _ := decodeFramer(syms, 1000, softCfg())
		if len(hm) > 0 && hm[0].ID() == "KA91" {
			hardOK++
		}
		if len(sm) > 0 && sm[0].ID() == "KA91" {
			softOK++
		}
	}
	t.Logf("at sigma 55 Hz (mark/space +-85 Hz): message KA91 found by hard %d of 12, soft %d of 12", hardOK, softOK)
	if softOK <= hardOK || softOK < 3 {
		t.Errorf("soft %d, hard %d", softOK, hardOK)
	}
}

func TestSoftChunkInvariance(t *testing.T) {
	syms := symbolsOf(encode(testText, 160), 60, 1, 3)
	_, ref, _ := decodeFramer(syms, len(syms), softCfg())
	for _, c := range []int{1, 7, 999} {
		_, got, _ := decodeFramer(syms, c, softCfg())
		if fmt.Sprint(got) != fmt.Sprint(ref) {
			t.Fatalf("chunk %d: events differ", c)
		}
	}
}

// Noise alone never locks and never gives text.
func TestSoftNoiseOnlyGivesNothing(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	syms := make([]fsk.Symbol, 60000)
	for i := range syms {
		syms[i] = fsk.Symbol{Soft: 85 * rng.NormFloat64(), Locked: true}
	}
	_, evs, text := decodeFramer(syms, 1000, softCfg())
	if text != "" || len(evs) != 0 {
		t.Errorf("noise gave %d events, text %q", len(evs), text)
	}
}

func TestSoftResyncAndReuse(t *testing.T) {
	d := NewSoftDecoder(softCfg())
	syms := symbolsOf(encode(testText, 160), 0, 1, 4)
	var n int
	for i := 0; i < len(syms)/2; i += 500 {
		n += len(d.Process(syms[i:min(i+500, len(syms)/2)]))
	}
	if !d.Locked() {
		t.Fatal("not locked half way through")
	}
	evs := d.Resync()
	if d.Locked() || len(evs) == 0 || evs[len(evs)-1].Kind != EvUnlock || evs[len(evs)-1].Reason != "resync" {
		t.Errorf("resync: locked %v, events %v", d.Locked(), evs)
	}
	var p navtex.Parser
	var msgs []navtex.Message
	for i := 0; i < len(syms); i += 500 {
		for _, e := range d.Process(syms[i:min(i+500, len(syms))]) {
			if e.Kind == EvText {
				msgs = append(msgs, p.Feed(e.Text)...)
			}
		}
	}
	for _, e := range d.Flush() {
		if e.Kind == EvText {
			msgs = append(msgs, p.Feed(e.Text)...)
		}
	}
	if len(msgs) != 0 && msgs[0].ID() == "" {
		t.Errorf("odd message after resync: %+v", msgs)
	}
}
