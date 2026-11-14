package when

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestLoadLocation(t *testing.T) {
	if l, err := LoadLocation(""); err != nil || l != time.Local {
		t.Errorf("empty = %v %v, want the computer's zone", l, err)
	}
	if l, err := LoadLocation("UTC"); err != nil || l != time.UTC {
		t.Errorf("UTC = %v %v", l, err)
	}
	if _, err := LoadLocation("Europe/Oslo"); err != nil {
		t.Errorf("Europe/Oslo: %v", err)
	}
	if _, err := LoadLocation("Mars/Olympus"); err == nil {
		t.Error("an unknown zone was accepted")
	}
}

func TestAddDaysIsCalendarArithmetic(t *testing.T) {
	cases := []struct {
		from Date
		n    int
		want Date
	}{
		{Date{2026, 3, 9}, -1, Date{2026, 3, 8}},
		{Date{2026, 3, 1}, -1, Date{2026, 2, 28}},
		{Date{2024, 3, 1}, -1, Date{2024, 2, 29}},
		{Date{2026, 1, 3}, -7, Date{2025, 12, 27}},
		{Date{2026, 12, 31}, 1, Date{2027, 1, 1}},
	}
	for _, c := range cases {
		if got := c.from.AddDays(c.n); got != c.want {
			t.Errorf("%v %+d = %v, want %v", c.from, c.n, got, c.want)
		}
	}
}

func TestWallClockChanges(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	utc := func(s string) time.Time {
		x, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	// An ordinary time, winter and summer.
	if got, want := Wall(Date{2026, 1, 15}, 12, 0, ny), utc("2026-01-15T17:00:00Z"); !got.Equal(want) {
		t.Errorf("winter noon = %v, want %v", got, want)
	}
	if got, want := Wall(Date{2026, 7, 15}, 12, 0, ny), utc("2026-07-15T16:00:00Z"); !got.Equal(want) {
		t.Errorf("summer noon = %v, want %v", got, want)
	}
	// 2026-03-08: the clocks go forward at 02:00. 02:30 does not exist and moves on by the gap.
	if got, want := Wall(Date{2026, 3, 8}, 2, 30, ny), utc("2026-03-08T07:30:00Z"); !got.Equal(want) {
		t.Errorf("02:30 in the gap = %v, want %v (03:30 EDT)", got, want)
	}
	// 2026-11-01: the clocks go back at 02:00; 01:30 happens twice and the first one is used.
	if got, want := Wall(Date{2026, 11, 1}, 1, 30, ny), utc("2026-11-01T05:30:00Z"); !got.Equal(want) {
		t.Errorf("01:30 twice = %v, want %v (the EDT one)", got, want)
	}
	// Days are 23 and 25 hours long on those dates.
	if d := EndOfDay(Date{2026, 3, 8}, ny).Sub(StartOfDay(Date{2026, 3, 8}, ny)); d != 23*time.Hour {
		t.Errorf("spring-forward day is %v", d)
	}
	if d := EndOfDay(Date{2026, 11, 1}, ny).Sub(StartOfDay(Date{2026, 11, 1}, ny)); d != 25*time.Hour {
		t.Errorf("fall-back day is %v", d)
	}
	// A zone ahead of UTC, and one with a half-hour offset.
	if got, want := Wall(Date{2026, 6, 1}, 0, 0, mustLoc(t, "Europe/Oslo")), utc("2026-05-31T22:00:00Z"); !got.Equal(want) {
		t.Errorf("Oslo midnight = %v, want %v", got, want)
	}
	if got, want := Wall(Date{2026, 6, 1}, 8, 0, mustLoc(t, "Asia/Kolkata")), utc("2026-06-01T02:30:00Z"); !got.Equal(want) {
		t.Errorf("Kolkata 08:00 = %v, want %v", got, want)
	}
}

func TestYesterdayAcrossTheClockChange(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08 21:00 EDT = 2026-03-09 01:00Z: "today" is already the 8th in New York but the 9th in UTC.
	now := time.Date(2026, 3, 9, 1, 0, 0, 0, time.UTC)
	if got := (DateRef{Relative: true, Back: 0}).Resolve(now, ny); got != (Date{2026, 3, 8}) {
		t.Errorf("today = %v", got)
	}
	if got := (DateRef{Relative: true, Back: 1}).Resolve(now, ny); got != (Date{2026, 3, 7}) {
		t.Errorf("yesterday = %v", got)
	}
	// Seven days back from the day after the change.
	if got := (DateRef{Relative: true, Back: 7}).Resolve(time.Date(2026, 3, 9, 15, 0, 0, 0, time.UTC), ny); got != (Date{2026, 3, 2}) {
		t.Errorf("today-7 = %v", got)
	}
}

func near(t *testing.T, name string, got time.Time, ok bool, want string, tol time.Duration) {
	t.Helper()
	if !ok {
		t.Errorf("%s: no event", name)
		return
	}
	w, err := time.Parse(time.RFC3339, want)
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Sub(w); d > tol || d < -tol {
		t.Errorf("%s = %s, want %s +-%v", name, got.UTC().Format(time.RFC3339), want, tol)
	}
}

func TestSunEvent(t *testing.T) {
	ny := Zone{Loc: mustLoc(t, "America/New_York"), Lat: 40.71, Lon: -74.0, HavePos: true}
	// New York on 2026-11-13 (NOAA solar calculator, checked separately): sunrise 11:40:45Z, sunset 21:39:46Z.
	r, ok := ny.SunEvent(Date{2026, 11, 13}, Sunrise)
	near(t, "NY sunrise", r, ok, "2026-11-13T11:40:45Z", 2*time.Minute)
	s, ok := ny.SunEvent(Date{2026, 11, 13}, Sunset)
	near(t, "NY sunset", s, ok, "2026-11-13T21:39:46Z", 2*time.Minute)

	// A local date whose sunrise falls on the previous UTC day (Auckland is 13 hours ahead in December).
	akl := Zone{Loc: mustLoc(t, "Pacific/Auckland"), Lat: -36.85, Lon: 174.76, HavePos: true}
	r, ok = akl.SunEvent(Date{2026, 12, 15}, Sunrise)
	if !ok {
		t.Fatal("Auckland: no sunrise")
	}
	if lr := r.In(akl.Loc); lr.Day() != 15 || lr.Hour() < 5 || lr.Hour() > 6 {
		t.Errorf("Auckland sunrise on the 15th is %v local", lr)
	}
	if r.UTC().Day() != 14 {
		t.Errorf("Auckland sunrise should fall on the previous UTC day, got %v", r.UTC())
	}

	// Sunset after local midnight (Reykjavik at midsummer, zone UTC+0): still one per local date.
	rk := Zone{Loc: mustLoc(t, "Atlantic/Reykjavik"), Lat: 64.15, Lon: -21.94, HavePos: true}
	s, ok = rk.SunEvent(Date{2026, 6, 21}, Sunset)
	if !ok {
		t.Fatal("Reykjavik: no sunset")
	}
	if h := s.In(rk.Loc).Hour(); h != 23 && h != 0 {
		t.Errorf("Reykjavik midsummer sunset at %v", s.In(rk.Loc))
	}
	if DateOf(s, rk.Loc) != (Date{2026, 6, 21}) {
		t.Errorf("sunset belongs to %v", DateOf(s, rk.Loc))
	}

	// Polar day and polar night, and no position.
	tr := Zone{Loc: mustLoc(t, "Europe/Oslo"), Lat: 69.65, Lon: 18.96, HavePos: true}
	if _, ok := tr.SunEvent(Date{2026, 6, 21}, Sunset); ok {
		t.Error("Tromso has a sunset at midsummer")
	}
	if _, ok := tr.SunEvent(Date{2026, 12, 21}, Sunrise); ok {
		t.Error("Tromso has a sunrise at midwinter")
	}
	if _, ok := (Zone{Loc: time.UTC}).SunEvent(Date{2026, 11, 13}, Sunset); ok {
		t.Error("a sun event without a position")
	}
}

func TestParseDateRef(t *testing.T) {
	good := map[string]DateRef{
		"today":            {Relative: true},
		"today-1":          {Relative: true, Back: 1},
		"local today - 7":  {Relative: true, Back: 7},
		" Today-3 ":        {Relative: true, Back: 3},
		"2026-11-13":       {Fixed: Date{2026, 11, 13}},
		"local 2026-11-13": {Fixed: Date{2026, 11, 13}},
	}
	for in, want := range good {
		got, err := ParseDateRef(in)
		if err != nil || got != want {
			t.Errorf("%q = %+v %v, want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"today-8", "today+1", "today-x", "yesterday", "2026-13-01", "13-11-2026"} {
		if _, err := ParseDateRef(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	for _, r := range []DateRef{{Relative: true}, {Relative: true, Back: 4}, {Fixed: Date{2026, 1, 2}}} {
		if back, err := ParseDateRef(r.String()); err != nil || back != r {
			t.Errorf("round trip of %v: %+v %v", r, back, err)
		}
	}
}

func TestParseTimeRef(t *testing.T) {
	good := map[string]TimeRef{
		"06:30":         {Hour: 6, Min: 30},
		"sunset":        {Sun: true, Event: Sunset},
		"sunset-3h":     {Sun: true, Event: Sunset, OffsetMin: -180},
		"sunset - 90m":  {Sun: true, Event: Sunset, OffsetMin: -90},
		"sunrise+1:30":  {Sun: true, Event: Sunrise, OffsetMin: 90},
		"local sunrise": {Sun: true, Event: Sunrise},
	}
	for in, want := range good {
		got, err := ParseTimeRef(in)
		if err != nil || got != want {
			t.Errorf("%q = %+v %v, want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"6:30", "24:00", "12:60", "sunset-4h", "sunset+", "sunsetx", "noon"} {
		if _, err := ParseTimeRef(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	for _, r := range []TimeRef{{Hour: 7, Min: 5}, {Sun: true, Event: Sunset}, {Sun: true, Event: Sunset, OffsetMin: -180}, {Sun: true, Event: Sunrise, OffsetMin: 45}} {
		if back, err := ParseTimeRef(r.String()); err != nil || back != r {
			t.Errorf("round trip of %v: %+v %v", r, back, err)
		}
	}
}

// "Last night": from sunset the evening before to an hour after sunrise this morning.
func TestLastNightWindow(t *testing.T) {
	z := Zone{Loc: mustLoc(t, "Europe/Oslo"), Lat: 59.91, Lon: 10.75, HavePos: true}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	from, ok1 := z.At((DateRef{Relative: true, Back: 1}).Resolve(now, z.Loc), TimeRef{Sun: true, Event: Sunset, OffsetMin: -60})
	to, ok2 := z.At((DateRef{Relative: true}).Resolve(now, z.Loc), TimeRef{Sun: true, Event: Sunrise, OffsetMin: 60})
	if !ok1 || !ok2 {
		t.Fatal("no sun events")
	}
	// Oslo, 2026-11-12/13 (NOAA calculator): sunset 14:53Z the evening before is about the same, sunrise 07:09Z.
	near(t, "from", from, true, "2026-11-12T13:55:00Z", 4*time.Minute)
	near(t, "to", to, true, "2026-11-13T08:09:00Z", 2*time.Minute)
	if h := to.Sub(from); h < 18*time.Hour || h > 19*time.Hour {
		t.Errorf("window is %v long", h)
	}
}
