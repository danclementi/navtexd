package engine

import (
	"testing"

	"navtex/internal/config"
	"navtex/internal/iq"
)

func TestRealSliceFollowsViewCenter(t *testing.T) {
	m := config.Default().Meters
	m.SpectrumMaxHz = 2700
	for _, c := range []struct{ shift, lo, hi float64 }{{1000, 0, 2700}, {5000, 3650, 6350}, {23500, 21300, 24000}} {
		s := newSpec(48000, iq.Real, c.shift, m)
		lo := float64(s.k0) * s.step
		hi := lo + float64(s.bins-1)*s.step
		if lo < c.lo-2*s.step || lo > c.lo+2*s.step || hi < c.hi-2*s.step || hi > c.hi+2*s.step || lo < 0 || hi > 24000 {
			t.Errorf("shift %v: slice %.0f..%.0f Hz, want about %.0f..%.0f", c.shift, lo, hi, c.lo, c.hi)
		}
	}
}
