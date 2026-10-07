package engine

import (
	"math"
	"math/rand"
	"testing"

	"navtex/internal/config"
	"navtex/internal/iq"
)

// The eye measure follows the decision signal-to-noise ratio: noise-free symbols give 100, a ratio equal to the low
// limit gives about 0, and it needs enough recent locked symbols.
func TestEyeQuality(t *testing.T) {
	e, err := New(48000, 517000, iq.Real, config.Default(), Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.eyeQuality(); ok {
		t.Error("available without symbols")
	}
	rng := rand.New(rand.NewSource(3))
	fill := func(snrLin float64) {
		sd := 85 / sqrt(snrLin)
		for i := 0; i < eyeN; i++ {
			a := 85.0
			if rng.Intn(2) == 0 {
				a = -85
			}
			e.eye[i] = a + rng.NormFloat64()*sd
		}
		e.eyeFill, e.eyeLastT = eyeN, e.Now()
	}
	fill(1e6)
	if q, ok := e.eyeQuality(); !ok || q < 99 {
		t.Errorf("clean: %v %v", q, ok)
	}
	fill(10) // 10 dB: (10-3)/12 = 58
	if q, _ := e.eyeQuality(); q < 45 || q > 72 {
		t.Errorf("10 dB: %v", q)
	}
	fill(1) // 0 dB: below the low limit
	if q, _ := e.eyeQuality(); q > 10 {
		t.Errorf("0 dB: %v", q)
	}
	e.eyeLastT = e.Now() - 10 // the symbols are old (not locked any more)
	if _, ok := e.eyeQuality(); ok {
		t.Error("stale symbols accepted")
	}
}

func sqrt(x float64) float64 { return math.Sqrt(x) }
