// Package when resolves the relative dates and times of the log filter ("local today-1", "local sunset - 1 h") to instants.
//
// Everything is calendar arithmetic in the receiver's time zone: a day is a calendar date, not 24 hours, so "yesterday" is right
// on the two days a year when the clocks change. A time zone is an IANA name (America/New_York), which carries its own daylight
// saving rules, so there is nothing else to configure.
package when

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // Windows has no zoneinfo database of its own
	"unicode"

	"navtex/internal/astro"
)

// MaxBackDays is the furthest "today-N" a filter offers.
const MaxBackDays = 7

// MaxSunOffsetMin is the furthest a sun-relative time may be from sunrise or sunset (minutes, either way).
const MaxSunOffsetMin = 180

// Zone is the receiver's place: its time zone and, when known, its position (needed for sunrise and sunset).
type Zone struct {
	Loc      *time.Location
	Lat, Lon float64
	HavePos  bool
}

// LoadLocation returns the zone called name (an IANA name); the empty name is the zone of the computer running the program.
func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	if strings.EqualFold(name, "utc") {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (an IANA name such as America/New_York or Europe/Oslo)", name)
	}
	return loc, nil
}

// Date is a calendar date, in a zone that the context supplies.
type Date struct {
	Y int
	M time.Month
	D int
}

// String is yyyy-mm-dd.
func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Y, int(d.M), d.D) }

// ParseDate reads yyyy-mm-dd.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return Date{}, fmt.Errorf("bad date %q (want yyyy-mm-dd)", s)
	}
	return Date{t.Year(), t.Month(), t.Day()}, nil
}

// DateOf is the calendar date of t in loc.
func DateOf(t time.Time, loc *time.Location) Date {
	y, m, d := t.In(loc).Date()
	return Date{y, m, d}
}

// AddDays moves a date by n calendar days (negative: earlier). It is date arithmetic, so it ignores clock changes.
func (d Date) AddDays(n int) Date {
	t := time.Date(d.Y, d.M, d.D+n, 12, 0, 0, 0, time.UTC)
	return Date{t.Year(), t.Month(), t.Day()}
}

// Wall turns a wall-clock time on a date in loc into an instant. Two awkward cases are decided the same way every time:
// a time that happens twice (the clocks go back) is the first of the two, and a time that does not happen (the clocks go
// forward) is moved on by the length of the gap, so 02:30 on a spring-forward day is 03:30.
func Wall(d Date, hour, min int, loc *time.Location) time.Time {
	wall := time.Date(d.Y, d.M, d.D, hour, min, 0, 0, time.UTC) // the reading, not yet an instant
	var best time.Time
	found := false
	// A zone's offset on the day is one of the offsets the day before or after has; try them all.
	for _, probe := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, off := wall.Add(probe).In(loc).Zone()
		t := wall.Add(-time.Duration(off) * time.Second)
		y, m, dd := t.In(loc).Date()
		h, mi, _ := t.In(loc).Clock()
		if y == d.Y && m == d.M && dd == d.D && h == hour && mi == min && (!found || t.Before(best)) {
			best, found = t, true
		}
	}
	if found {
		return best
	}
	// In the gap: use the offset in force before it.
	_, off := wall.Add(-24 * time.Hour).In(loc).Zone()
	return wall.Add(-time.Duration(off) * time.Second)
}

// StartOfDay and EndOfDay bound a calendar date in loc: the end is the start of the next day (exclusive).
func StartOfDay(d Date, loc *time.Location) time.Time { return Wall(d, 0, 0, loc) }
func EndOfDay(d Date, loc *time.Location) time.Time   { return Wall(d.AddDays(1), 0, 0, loc) }

// Event is a daily sun event.
type Event int

const (
	Sunrise Event = iota
	Sunset
)

// SunEvent returns the sunrise or sunset that falls on local calendar date d at the position. ok is false when the sun does not
// rise or set on that date (polar day or night) or the position is not known.
func (z Zone) SunEvent(d Date, ev Event) (time.Time, bool) {
	if !z.HavePos {
		return time.Time{}, false
	}
	// astro works on UTC days; the event of local date d can fall on the UTC day before or after it.
	for _, n := range []int{-1, 0, 1} {
		day := time.Date(d.Y, d.M, d.D+n, 12, 0, 0, 0, time.UTC)
		rise, set, ok := astro.SunTimes(day, z.Lat, z.Lon)
		if !ok {
			continue
		}
		t := rise
		if ev == Sunset {
			t = set
		}
		if DateOf(t, z.Loc) == d {
			return t, true
		}
	}
	return time.Time{}, false
}

// DateRef is a date of the filter: a fixed date, or a number of days back from today in the receiver's zone.
type DateRef struct {
	Relative bool
	Back     int  // days before today, 0 to MaxBackDays (Relative only)
	Fixed    Date // the date (not Relative)
}

// ParseDateRef reads "yyyy-mm-dd", "today" or "today-3" (the word "local" in front is accepted).
func ParseDateRef(s string) (DateRef, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSpace(strings.TrimPrefix(s, "local"))
	if strings.HasPrefix(s, "today") {
		rest := strings.TrimSpace(strings.TrimPrefix(s, "today"))
		n := 0
		if rest != "" {
			if rest[0] != '-' {
				return DateRef{}, fmt.Errorf("bad relative date %q (want today or today-N)", s)
			}
			if _, err := fmt.Sscanf(strings.TrimSpace(rest[1:]), "%d", &n); err != nil || n < 0 || n > MaxBackDays {
				return DateRef{}, fmt.Errorf("bad relative date %q (today to today-%d)", s, MaxBackDays)
			}
		}
		return DateRef{Relative: true, Back: n}, nil
	}
	d, err := ParseDate(s)
	return DateRef{Fixed: d}, err
}

// String is the form ParseDateRef reads: "today-2" or "2026-11-13".
func (r DateRef) String() string {
	switch {
	case !r.Relative:
		return r.Fixed.String()
	case r.Back == 0:
		return "today"
	}
	return fmt.Sprintf("today-%d", r.Back)
}

// Resolve gives the calendar date, taking "today" from now in loc.
func (r DateRef) Resolve(now time.Time, loc *time.Location) Date {
	if !r.Relative {
		return r.Fixed
	}
	return DateOf(now, loc).AddDays(-r.Back)
}

// TimeRef is a time of day of the filter: a clock time, or an offset from sunrise or sunset.
type TimeRef struct {
	Sun       bool
	Event     Event // Sun only
	OffsetMin int   // minutes after the event, negative before (Sun only)
	Hour, Min int   // the clock time (not Sun)
}

// ParseTimeRef reads "hh:mm", "sunset", "sunset-90m", "sunrise+1h" or "sunset+1:30" (minutes after the event, hours or h:mm).
func ParseTimeRef(s string) (TimeRef, error) {
	s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "local")))
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	for name, ev := range map[string]Event{"sunrise": Sunrise, "sunset": Sunset} {
		if !strings.HasPrefix(s, name) {
			continue
		}
		rest := s[len(name):]
		if rest == "" {
			return TimeRef{Sun: true, Event: ev}, nil
		}
		sign := 1
		switch rest[0] {
		case '+':
		case '-':
			sign = -1
		default:
			return TimeRef{}, fmt.Errorf("bad sun time %q (want sunset, sunset-1h, sunrise+90m)", s)
		}
		min, err := parseSpan(rest[1:])
		if err != nil || min > MaxSunOffsetMin {
			return TimeRef{}, fmt.Errorf("bad sun time %q (offset up to %d minutes)", s, MaxSunOffsetMin)
		}
		return TimeRef{Sun: true, Event: ev, OffsetMin: sign * min}, nil
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(s) != 5 {
		return TimeRef{}, fmt.Errorf("bad time %q (hh:mm or sunrise/sunset ± offset)", s)
	}
	return TimeRef{Hour: h, Min: m}, nil
}

// parseSpan reads "90m", "1h" or "1:30".
func parseSpan(s string) (int, error) {
	var a, b int
	switch {
	case strings.HasSuffix(s, "m"):
		if _, err := fmt.Sscanf(s, "%dm", &a); err != nil {
			return 0, err
		}
		return a, nil
	case strings.HasSuffix(s, "h"):
		if _, err := fmt.Sscanf(s, "%dh", &a); err != nil {
			return 0, err
		}
		return a * 60, nil
	case strings.Contains(s, ":"):
		if _, err := fmt.Sscanf(s, "%d:%d", &a, &b); err != nil || b < 0 || b > 59 {
			return 0, fmt.Errorf("bad span %q", s)
		}
		return a*60 + b, nil
	}
	return 0, fmt.Errorf("bad span %q", s)
}

// String is the form ParseTimeRef reads.
func (r TimeRef) String() string {
	if !r.Sun {
		return fmt.Sprintf("%02d:%02d", r.Hour, r.Min)
	}
	name := "sunrise"
	if r.Event == Sunset {
		name = "sunset"
	}
	switch {
	case r.OffsetMin == 0:
		return name
	case r.OffsetMin%60 == 0:
		return fmt.Sprintf("%s%+dh", name, r.OffsetMin/60)
	}
	return fmt.Sprintf("%s%+dm", name, r.OffsetMin)
}

// At gives the instant of a time of day on a date. ok is false for a sun time on a date with no such event.
func (z Zone) At(d Date, t TimeRef) (time.Time, bool) {
	if !t.Sun {
		return Wall(d, t.Hour, t.Min, z.Loc), true
	}
	ev, ok := z.SunEvent(d, t.Event)
	if !ok {
		return time.Time{}, false
	}
	return ev.Add(time.Duration(t.OffsetMin) * time.Minute).Truncate(time.Minute), true
}
