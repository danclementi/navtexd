package geo

import (
	"math"
	"testing"
)

func TestDistanceBearing(t *testing.T) {
	// London (51.5, -0.12) to New York (40.71, -74.0): about 5570 km, initial bearing about 288 degrees.
	if d := DistanceKm(51.5, -0.12, 40.71, -74.0); math.Abs(d-5570) > 30 {
		t.Errorf("distance %.0f", d)
	}
	if b := BearingDeg(51.5, -0.12, 40.71, -74.0); math.Abs(b-288) > 2 {
		t.Errorf("bearing %.1f", b)
	}
	if d := DistanceKm(10, 20, 10, 20); d != 0 {
		t.Errorf("same point %v", d)
	}
	if b := BearingDeg(0, 0, 10, 0); math.Abs(b) > 1e-6 {
		t.Errorf("north bearing %v", b)
	}
}
