package pipeline

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"navtex/internal/config"
	"navtex/internal/iq"
	"navtex/internal/navtex"
	"navtex/internal/testvec"
)

type meta struct {
	CentreHz float64           `json:"centre_hz"`
	Truth    map[string]string `json:"truth"`
	Channels map[string]struct {
		Messages []struct {
			Body string `json:"body"`
		} `json:"messages"`
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

func run(t *testing.T, name string, chHz float64, blocks int) ([]navtex.Message, Summary) {
	var m meta
	testvec.JSON(t, name+".json", &m)
	w, err := iq.OpenWav(testvec.Path(t, name+".wav"), iq.WavOptions{CentreHz: m.CentreHz, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var msgs []navtex.Message
	sum, err := Run(context.Background(), w, testCfg(chHz), time.Time{}, func(e Event) {
		if e.Kind == EvMessage && e.Message.Complete {
			msgs = append(msgs, e.Message)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return msgs, sum
}

var exact = map[string]bool{"clean": true, "lowmark": true, "msb": true, "usb_real": true}

// Level 6: whole chain from the WAV. Cases the reference decodes exactly must decode exactly; the stress cases
// may not be worse than the reference by more than a couple of characters.
func TestEndToEnd(t *testing.T) {
	for _, name := range []string{"clean", "nominal", "lowmark", "msb", "impulsive", "weak", "usb_real"} {
		var m meta
		testvec.JSON(t, name+".json", &m)
		for ch, info := range m.Channels {
			t.Run(name+"_"+ch, func(t *testing.T) {
				var chHz float64
				fmt.Sscan(ch, &chHz)
				msgs, sum := run(t, name, chHz, 16384)
				var p navtex.Parser
				truth := p.Feed(m.Truth[ch])[0]
				refErr := 1 << 20
				if len(info.Messages) > 0 {
					refErr = lev(norm(info.Messages[0].Body), norm(truth.Body))
				}
				if len(msgs) == 0 {
					if refErr == 1<<20 {
						t.Skip("reference decoded nothing either")
					}
					t.Fatalf("no message (reference %d wrong chars); summary %+v", refErr, sum)
				}
				got := lev(norm(msgs[0].Body), norm(truth.Body))
				t.Logf("id %s, wrong chars %d (reference %d), locked symbols %d, f0 %.1f, order %v",
					msgs[0].ID(), got, refErr, sum.SymbolsLocked, sum.FinalF0, sum.Order)
				if exact[name] && (got != 0 || msgs[0].ID() != truth.ID()) {
					t.Errorf("expected exact; got %s %q", msgs[0].ID(), msgs[0].Body)
				}
				// Stress cases: the carrier trajectory differs from the reference's batch estimate, so a few marginal
				// symbols fall the other way (see TestBenchVsReference for the statistics).
				slack := 2
				if name == "weak" {
					slack = 8
				}
				if got > refErr+slack {
					t.Errorf("%d wrong chars, reference %d", got, refErr)
				}
			})
		}
	}
}

func TestRunErrors(t *testing.T) {
	// 518 kHz is not inside a 62.5 kHz-wide recording centred on 100 kHz
	w, err := iq.OpenWav(testvec.Path(t, "clean.wav"), iq.WavOptions{CentreHz: 100000, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := Run(context.Background(), w, testCfg(518000), time.Time{}, func(Event) {}); err == nil {
		t.Fatal("expected an out-of-band error")
	}
}

// Decoded characters are stamped with the moment they were sent (symbol position), not when they were decoded.
func TestTextTimestamps(t *testing.T) {
	var m meta
	testvec.JSON(t, "clean.json", &m)
	w, err := iq.OpenWav(testvec.Path(t, "clean.wav"), iq.WavOptions{CentreHz: m.CentreHz, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	start := time.Date(2026, 1, 26, 10, 38, 59, 0, time.UTC)
	var text strings.Builder
	var zTime float64 = -1
	_, err = Run(context.Background(), w, testCfg(518000), start, func(e Event) {
		if e.Kind != EvText {
			return
		}
		if !e.Wall.Equal(start.Add(time.Duration(e.Time * float64(time.Second)))) {
			t.Fatalf("Wall %v does not match Start + Time %.3f", e.Wall, e.Time)
		}
		text.WriteString(e.Text)
		if zTime < 0 && strings.HasSuffix(text.String(), "ZCZC") {
			zTime = e.Time
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "ZCZC FE01") || !strings.Contains(text.String(), "NNNN") {
		t.Fatalf("text stream lacks ZCZC/NNNN: %q", text.String())
	}
	// The generator starts the 518 kHz station at 2.0 s, sends 28 phasing pairs and a LTRS shift (58 slots of 70 ms),
	// so the first Z is at 6.08 s and the fourth character of ZCZC, one 140 ms slot pair apart each, at 6.50 s.
	if math.Abs(zTime-6.50) > 0.05 {
		t.Errorf("ZCZC completed at %.3f s, expected 6.50 s", zTime)
	}
}

func testCfg(chHz float64) config.Config {
	c := config.Default()
	c.Channel.FrequencyHz = chHz
	return c
}
