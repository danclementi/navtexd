package astro

import (
	"math"
	"testing"
	"time"
)

func near(t *testing.T, name string, got, want time.Time, tol time.Duration) {
	t.Helper()
	if d := got.Sub(want); d < -tol || d > tol {
		t.Errorf("%s = %s, want %s +-%v", name, got.Format("15:04"), want.Format("15:04"), tol)
	}
}

func TestSunTimes(t *testing.T) {
	// Greenwich, 2026-06-21: sunrise about 03:43 UTC, sunset about 20:21 UTC.
	r, s, ok := SunTimes(time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), 51.4769, 0)
	if !ok {
		t.Fatal("no sun times")
	}
	near(t, "rise", r, time.Date(2026, 6, 21, 3, 43, 0, 0, time.UTC), 4*time.Minute)
	near(t, "set", s, time.Date(2026, 6, 21, 20, 21, 0, 0, time.UTC), 4*time.Minute)
	// Equator equinox: rise about 06:03, set about 18:10 (UTC, lon 0).
	r, s, _ = SunTimes(time.Date(2026, 3, 20, 5, 0, 0, 0, time.UTC), 0, 0)
	near(t, "eq rise", r, time.Date(2026, 3, 20, 6, 3, 0, 0, time.UTC), 5*time.Minute)
	near(t, "eq set", s, time.Date(2026, 3, 20, 18, 10, 0, 0, time.UTC), 5*time.Minute)
	if _, _, ok := SunTimes(time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), 80, 0); ok {
		t.Error("polar night should have no sunrise")
	}
}

func TestElevation(t *testing.T) {
	// At the equator on the equinox the sun is nearly overhead at local solar noon.
	if e := Elevation(time.Date(2026, 3, 20, 12, 8, 0, 0, time.UTC), 0, 0); math.Abs(e-90) > 3 {
		t.Errorf("noon elevation %.1f", e)
	}
	if e := Elevation(time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), 0, 0); e > -80 {
		t.Errorf("midnight elevation %.1f", e)
	}
}
