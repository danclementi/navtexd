package schedule

import (
	"testing"
	"time"

	"navtex/internal/when"
)

func zoneNY(t *testing.T) when.Zone {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone database")
	}
	return when.Zone{Loc: loc, Lat: 40.32, Lon: -75.07, HavePos: true}
}

func TestValidate(t *testing.T) {
	ok := Entry{Param: "operator.comment", Zone: "local", At: "sunset-1h", Value: "dipole"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]Entry{
		"no setting":      {Zone: "utc", At: "10:00"},
		"bad zone":        {Param: "p", Zone: "est", At: "10:00"},
		"bad time":        {Param: "p", Zone: "utc", At: "25:00"},
		"sun in utc":      {Param: "p", Zone: "utc", At: "sunrise"},
		"midnight in utc": {Param: "p", Zone: "utc", At: "midnight+1h"},
		"two lines":       {Param: "p", Zone: "utc", At: "10:00", Value: "a\nb"},
	} {
		if e.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, at := range []string{"midnight", "midnight-2h", "midnight+30m", "sunrise+90m", "00:00"} {
		if err := (Entry{Param: "p", Zone: "local", At: at}).Validate(); err != nil {
			t.Errorf("%s: %v", at, err)
		}
	}
}

func TestMidnightAndFixedTimes(t *testing.T) {
	z := zoneNY(t)
	// 2026-11-17 is standard time (UTC-5): local midnight is 05:00 UTC
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	for at, want := range map[string]string{"midnight": "05:00", "midnight-1h": "04:00", "midnight+30m": "05:30", "18:00": "23:00"} {
		ts := Entry{Param: "p", Zone: "local", At: at}.Between(z, from, to)
		if len(ts) != 1 || ts[0].UTC().Format("15:04") != want {
			t.Errorf("%s: %v, want %s UTC", at, ts, want)
		}
	}
	if ts := (Entry{Param: "p", Zone: "utc", At: "18:00"}).Between(z, from, to); len(ts) != 1 || ts[0].UTC().Format("15:04") != "18:00" {
		t.Errorf("utc 18:00: %v", ts)
	}
}

func TestSunTimes(t *testing.T) {
	z := zoneNY(t)
	from := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	set := Entry{Param: "p", Zone: "local", At: "sunset"}.Between(z, from, from.Add(24*time.Hour))
	hour := set[0].In(z.Loc).Hour()
	if len(set) != 1 || hour < 16 || hour > 17 { // sunset in eastern Pennsylvania in mid November is about 16:45 EST
		t.Errorf("sunset %v", set)
	}
	if _, ok := (Entry{Param: "p", Zone: "local", At: "sunset"}).Next(when.Zone{Loc: z.Loc}, from); ok {
		t.Error("a sun time without a position has an instant")
	}
}

func TestDueAndCurrent(t *testing.T) {
	z := zoneNY(t)
	es := []Entry{
		{ID: 1, Param: "operator.comment", Zone: "utc", At: "10:00", Value: "day"},
		{ID: 2, Param: "operator.comment", Zone: "utc", At: "22:00", Value: "night"},
		{ID: 3, Param: "channel.comment", Zone: "utc", At: "12:00", Value: "x", Off: true},
	}
	now := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	due := Due(es, z, now.Add(-6*time.Hour), now)
	if len(due) != 1 || due[0].E.Value != "day" {
		t.Errorf("due %v", due)
	}
	cur := Current(es, z, now)
	if len(cur) != 1 || cur["operator.comment"].E.Value != "day" {
		t.Errorf("current %v", cur)
	}
	// before the first entry of the day, the evening one of the day before is in force
	if c := Current(es, z, time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)); c["operator.comment"].E.Value != "night" {
		t.Errorf("early: %v", c)
	}
	// the same minute: the later entry of the list wins
	same := []Entry{{ID: 1, Param: "p", Zone: "utc", At: "10:00", Value: "a"}, {ID: 2, Param: "p", Zone: "utc", At: "10:00", Value: "b"}}
	if c := Current(same, z, now); c["p"].E.Value != "b" {
		t.Errorf("same minute: %v", c)
	}
}

func TestStoreKeepsEntries(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if err := s.Replace([]Entry{{Param: "operator.comment", Zone: "utc", At: "10:00", Value: "a"}, {ID: 7, Param: "operator.comment", Zone: "local", At: "midnight", Value: "b"}}); err != nil {
		t.Fatal(err)
	}
	got := Open(dir).All()
	if len(got) != 2 || got[1].ID != 7 || got[0].ID != 8 || got[0].Value != "a" {
		t.Errorf("reopened: %+v", got)
	}
	if err := s.Replace([]Entry{{Param: "p", Zone: "utc", At: "99:00"}}); err == nil {
		t.Error("a bad time was stored")
	}
	if len(Open(dir).All()) != 2 {
		t.Error("a refused change altered the file")
	}
}
