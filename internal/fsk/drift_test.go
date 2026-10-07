package fsk

import (
	"math"
	"math/rand"
	"testing"
)

// A transmitter whose symbol clock runs fast or slow relative to ours makes the timing offset drift without bound;
// the demodulator must follow it for minutes without losing the sample history it still needs.
func TestDemodClockDrift(t *testing.T) {
	for _, ppm := range []float64{-2000, 1500} {
		const fs = 4000.0
		rng := rand.New(rand.NewSource(1))
		d := NewDemod(DefaultParams(fs), 0)
		baud := 100 * (1 + ppm*1e-6)
		var ph float64
		sec := 240
		locked := 0
		total := 0
		for s := 0; s < sec; s++ {
			x := make([]complex128, int(fs))
			for i := range x {
				tt := float64(s*int(fs)+i) / fs
				bit := rng.Int63() // per-symbol bit chosen by hashing the symbol index
				_ = bit
				m := int(tt * baud)
				f := -85.0
				if (uint32(m)*2654435761>>13)&1 == 1 {
					f = 85
				}
				ph += 2 * math.Pi * f / fs
				x[i] = complex(math.Cos(ph), math.Sin(ph))
			}
			for _, y := range d.Process(x) {
				total++
				if y.Locked {
					locked++
				}
			}
		}
		if locked < total/2 {
			t.Errorf("ppm %v: only %d of %d symbols locked", ppm, locked, total)
		}
	}
}
