package fsk

import (
	"math"
	"testing"
)

// A carrier placed half a shift off (as when a search window caught one tone) must be corrected by the periodic
// re-check, and the tracker must not walk further than track_max_excursion_hz from where the search put it.
func TestWrongCarrierIsRecovered(t *testing.T) {
	const fs = 4000.0
	const truth = 10.0
	r := NewReceiver(DefaultReceiverConfig(fs))
	r.setF0(truth-85, true) // wrong by one half shift
	r.haveCarr = true
	var ph float64
	for s := 0; s < 40; s++ {
		x := make([]complex128, int(fs))
		for i := range x {
			m := int(float64(s*int(fs)+i) / fs * 100)
			f := truth - 85
			if (uint32(m)*2654435761>>13)&1 == 1 {
				f = truth + 85
			}
			ph += 2 * math.Pi * f / fs
			x[i] = complex(math.Cos(ph), math.Sin(ph))
		}
		r.Process(x)
	}
	if d := math.Abs(r.F0() - truth); d > 15 {
		t.Errorf("carrier offset %.1f Hz after 40 s, want near %.0f", r.F0(), truth)
	}
}

func TestTrackerExcursionIsLimited(t *testing.T) {
	r := NewReceiver(DefaultReceiverConfig(4000))
	r.setF0(0, true)
	r.haveCarr = true
	for i := 0; i < 20; i++ {
		r.ring = r.ring[:0]
		for j := 0; j < 300; j++ { // every symbol claims a +3 bias: persistent push upward (+6 bias)
			if j%2 == 0 {
				r.ring = append(r.ring, 14)
			} else {
				r.ring = append(r.ring, -2)
			}
		}
		r.track()
	}
	if f := r.F0(); f > 30.01 || f < 29 {
		t.Errorf("tracker moved the carrier to %.1f Hz, want it stopped at the 30 Hz excursion limit", f)
	}
}
