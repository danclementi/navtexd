package sitor

import (
	"math/rand"
	"testing"

	"navtex/internal/fsk"
)

// synth builds a SITOR-B like symbol stream: DX words in the even slots, each repeated 5 slots later, soft values +-85 Hz.
func synth(words int, seed int64) []fsk.Symbol {
	rng := rand.New(rand.NewSource(seed))
	w := make([]uint8, words)
	for n := range w {
		if n%2 == 1 && n >= 5 {
			w[n] = w[n-5]
		} else {
			w[n] = validPatterns[rng.Intn(len(validPatterns))]
		}
	}
	var out []fsk.Symbol
	for _, m := range w {
		for t := 0; t < 7; t++ {
			v := -85.0
			if m>>t&1 == 1 {
				v = 85
			}
			out = append(out, fsk.Symbol{Soft: v, Locked: true})
		}
	}
	return out
}

// After a fade the framing comes back after RelockWords valid words instead of lock_words, at the same alignment.
func TestQuickRelockAfterFade(t *testing.T) {
	syms := synth(600, 1)
	const gapAt, gap = 1400, 140 // symbols; 140 is a multiple of 7
	for _, c := range []struct{ relock, min, max int }{{0, 7 * 9, 7*10 + 7}, {4, 0, 7*4 + 7}} {
		cfg := DefaultConfig()
		cfg.RelockWords = c.relock
		d := NewDecoder(cfg)
		unlocks, back, firstAlign := 0, -1, -1
		for i, s := range syms {
			if i >= gapAt && i < gapAt+gap {
				s.Soft = 0
			}
			for _, e := range d.Process([]fsk.Symbol{s}) {
				switch e.Kind {
				case EvLock:
					if firstAlign < 0 {
						firstAlign = e.Align
					} else if unlocks > 0 && back < 0 {
						back = i - (gapAt + gap)
						if e.Align != firstAlign {
							t.Errorf("relock %d: alignment %d, was %d", c.relock, e.Align, firstAlign)
						}
					}
				case EvUnlock:
					unlocks++
				}
			}
		}
		if unlocks == 0 || back < 0 {
			t.Fatalf("relock %d: unlocks %d, back %d", c.relock, unlocks, back)
		}
		if back > c.max || back < c.min {
			t.Errorf("relock %d: framing back %d symbols after the fade, want %d..%d", c.relock, back, c.min, c.max)
		}
	}
}

// The quick re-lock is only for a while after the lock ended, and a Resync forgets it.
func TestRelockWindowAndResync(t *testing.T) {
	syms := synth(900, 2)
	cfg := DefaultConfig()
	cfg.RelockWindowS = 3 // 300 symbols
	d := NewDecoder(cfg)
	locks := 0
	feed := func(s fsk.Symbol) {
		for _, e := range d.Process([]fsk.Symbol{s}) {
			if e.Kind == EvLock {
				locks++
			}
		}
	}
	i := 0
	for ; i < 1000; i++ {
		feed(syms[i])
	}
	for ; i < 1000+700; i++ { // 7 s of nothing: far beyond the window
		feed(fsk.Symbol{})
	}
	locks = 0
	back := -1
	for k := 0; k < 300 && back < 0; k++ {
		feed(syms[i+k])
		if locks > 0 {
			back = k
		}
	}
	if back < 7*10-7 {
		t.Errorf("quick re-lock after the window: back after %d symbols", back)
	}
	d.Resync()
	if d.last.ok {
		t.Error("resync kept the last framing")
	}
}
