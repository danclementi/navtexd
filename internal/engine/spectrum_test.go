package engine

import (
	"testing"

	"navtex/internal/config"
	"navtex/internal/iq"
)

func TestIQSliceStaysInsideSpan(t *testing.T) {
	m := config.Meters{RateHz: 10, SpectrumMaxHz: 40000, SpectrumSmooth: 0.85}
	s := newSpec(48000, iq.IQ, 14000, m) // carrier far from the centre of a narrow stream
	lo, hi := float64(s.k0)*s.step, float64(s.k0+s.bins)*s.step
	if lo < -24000-s.step || hi > 24000+s.step {
		t.Errorf("slice %.0f..%.0f Hz leaves the received span", lo, hi)
	}
}
