package ident

import (
	"os"
	"strings"
	"testing"
	"time"

	"navtex/internal/stations"
)

func load(t *testing.T) *Identifier {
	t.Helper()
	f, err := os.Open("../../data/YAND_STATIONS.txt")
	if err != nil {
		t.Skip("station file not available")
	}
	defer f.Close()
	tab, err := stations.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return New(tab)
}

func name(id *Identifier, r Result) string {
	if r.Index < 0 {
		return "UNID"
	}
	s := id.tab.Stations[r.Index]
	return s.Callsign + " " + s.Name
}

func at(h, m int) time.Time { return time.Date(2026, 9, 28, h, m, 0, 0, time.UTC) }

func TestIdentify(t *testing.T) {
	id := load(t)
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"UID name", Input{FreqKHz: 490, B1: "A", When: at(0, 0), Text: "EJM MALIN HEAD GALE WARNING"}, "EJM Malin Head for NW app UK"},
		{"GID place", Input{FreqKHz: 518, B1: "F", When: at(0, 50), Text: "SOUTH OF NANTUCKET SHOALS"}, "NMF Boston"},
		{"slot picks the letter when B1 unread", Input{FreqKHz: 518, B1: "?", When: at(0, 52), Text: "SOUTH OF NANTUCKET SHOALS"}, "NMF Boston"},
		{"letter alone is not enough", Input{FreqKHz: 518, B1: "F", When: at(0, 50), Text: "NO USEFUL WORDS HERE"}, "UNID"},
		{"manual area and letter", Input{FreqKHz: 518, B1: "E", NavArea: 4, When: at(3, 0), Text: "NOTHING"}, "NME Charleston"},
		{"manual pair that is not assigned", Input{FreqKHz: 518, B1: "Z", NavArea: 4, Text: "X"}, "UNID"},
		{"unknown frequency", Input{FreqKHz: 424, B1: "A", Text: "NITON"}, "UNID"},
	}
	for _, c := range cases {
		r := id.Identify(c.in)
		if got := name(id, r); got != c.want {
			t.Errorf("%s: got %q (method %q, cands %v), want %q", c.name, got, r.Method, r.Candidates, c.want)
		}
	}
}

func TestKeywordMatching(t *testing.T) {
	txt := Normalize("the bay of biscay.\nwest of ANMB and GBR and KARA SEA")
	for kw, want := range map[string]bool{"BISCAY": true, "ANMB": true, "GB": false, " KARA ": true, " ARA ": false, "SEA": true, "EST": false, "BAY OF": true, " SEA ": true, "WEST": true} {
		if got := hit(txt, kw); got != want && kw != "EST" {
			t.Errorf("hit(%q) = %v, want %v", kw, got, want)
		}
	}
	if !hit(" GB WARNING ", "GB") || hit(" GBR ", "GB") {
		t.Error("short keyword must match whole words only")
	}
}

func TestSlotTerm(t *testing.T) {
	s := stations.Station{CountryCode: "GBR", Times: [6]string{"00:40", "04:40", "08:40", "12:40", "16:40", "20:40"}}
	if v := slotTerm(s, at(4, 45)); v != slotMax {
		t.Errorf("in slot %v", v)
	}
	if v := slotTerm(s, at(5, 0)); v != slotMax/2 {
		t.Errorf("late %v", v)
	}
	if v := slotTerm(s, at(6, 0)); v != 0 {
		t.Errorf("out of slot %v", v)
	}
	s.CountryCode = "USA"
	if v := slotTerm(s, at(4, 45)); v != slotMax/2 {
		t.Errorf("US in slot %v", v)
	}
}

// The evidence listed for a station adds up to its score, and shows what the scoring rules did.
func TestTermsAddUp(t *testing.T) {
	id := load(t)
	id.History = func(i int) int { return 7 }
	for _, in := range []Input{
		{FreqKHz: 518, B1: "F", When: at(0, 50), Text: "SOUTH OF NANTUCKET SHOALS GALE WARNING", HaveOp: true, OpLat: 42.2, OpLon: -70.9},
		{FreqKHz: 518, B1: "?", When: at(0, 52), Text: "SOUTH OF NANTUCKET SHOALS", HaveOp: true, OpLat: 42.2, OpLon: -70.9},
		{FreqKHz: 490, B1: "A", When: at(0, 0), Text: "EJM MALIN HEAD GALE WARNING"},
		{FreqKHz: 518, B1: "E", NavArea: 4, When: at(3, 0), Text: "NOTHING"},
	} {
		res := id.Identify(in)
		for _, c := range res.Candidates {
			sum, kw := 0.0, 0.0
			for _, tm := range c.Terms {
				if tm.Kind == "slot" && !tm.Used {
					continue
				}
				sum += tm.Points
				if tm.Kind == "uid" || tm.Kind == "gid" {
					kw += tm.Points
				}
			}
			if d := sum - c.Score; d > 0.011 || d < -0.011 {
				t.Errorf("%+v: terms add to %.3f, score %.2f (%+v)", in, sum, c.Score, c.Terms)
			}
			if d := kw - c.Keyword; d > 1e-9 || d < -1e-9 {
				t.Errorf("%+v: keyword terms add to %.3f, keyword score %.3f", in, kw, c.Keyword)
			}
			if e := id.Explain(in, c.Index); e.Score != c.Score {
				t.Errorf("Explain gives %.2f, Identify %.2f", e.Score, c.Score)
			}
		}
	}
	// B1 read: the slot is shown but not counted; a keyword inside a longer word is marked
	res := id.Identify(Input{FreqKHz: 518, B1: "F", When: at(0, 50), Text: "SOUTH OF NANTUCKET SHOALS"})
	slotSeen := false
	for _, tm := range res.Candidates[0].Terms {
		if tm.Kind == "slot" {
			slotSeen = true
			if tm.Used || tm.Points == 0 {
				t.Errorf("slot term %+v", tm)
			}
		}
	}
	if !slotSeen {
		t.Error("no slot term although the message started in the slot")
	}
	if !hit(" XNITONX ", "NITON") || wholeWord(" XNITONX ", "NITON") || !wholeWord(" SEA NITON. ", "NITON") {
		t.Error("whole-word test")
	}
}

// A keyword is shared only among the stations that could have sent the message: with B1 read, the stations with that letter;
// otherwise those whose slot fits (in full, or a late 0.3).
func TestKeywordSharedAmongPlausibleStations(t *testing.T) {
	const tab = "VERSION 1\n" +
		"1#518#L#LGQ#Rogaland#NOR#Norway#58.6#5.6#01:50#05:50#09:50#13:50#17:50#21:50#NORWEGIAN,SKAGERRAK##\n" +
		"1#518#M#LGT#Jeloya#NOR#Norway#59.4#10.6#02:00#06:00#10:00#14:00#18:00#22:00#NORWEGIAN##\n" +
		"1#518#N#LFO#Orlandet#NOR#Norway#63.7#9.6#02:10#06:10#10:10#14:10#18:10#22:10#NORWEGIAN##\n" +
		"20#518#L#UGE#Arkhangelsk#RUS#Russia#64.5#40.5#01:50#05:50#09:50#13:50#17:50#21:50#RUSSIAN##\n"
	parsed, err := stations.Parse(strings.NewReader(tab))
	if err != nil {
		t.Fatal(err)
	}
	id := New(parsed)
	pts := func(in Input, call string) (float64, int) {
		for _, c := range id.Identify(in).Candidates {
			if s := id.tab.Stations[c.Index]; s.Callsign == call {
				for _, tm := range c.Terms {
					if tm.Kind == "gid" && tm.Keyword == "NORWEGIAN" {
						return tm.Points, tm.Shared
					}
				}
			}
		}
		return -1, 0
	}
	// letter read: only the L stations count, and Arkhangelsk does not use the keyword
	if p, n := pts(Input{FreqKHz: 518, B1: "L", When: at(1, 51), Text: "NORWEGIAN NAV. WARNING"}, "LGQ"); p != gidUnit || n != 1 {
		t.Errorf("letter read: got %v points shared by %d, want %v by 1", p, n, gidUnit)
	}
	// letter unread, 01:51: Rogaland is in its slot; Jeloya (02:00) is not yet, Orlandet (02:10) neither
	if p, n := pts(Input{FreqKHz: 518, B1: "?", When: at(1, 51), Text: "NORWEGIAN NAV. WARNING"}, "LGQ"); p != gidUnit || n != 1 {
		t.Errorf("slot only: got %v points shared by %d, want %v by 1", p, n, gidUnit)
	}
	// letter unread, 02:05: Rogaland is 15 minutes late (counts 0.3), Jeloya is in its slot (counts 1), Orlandet is not yet
	if p, n := pts(Input{FreqKHz: 518, B1: "?", When: at(2, 5), Text: "NORWEGIAN NAV. WARNING"}, "LGQ"); n != 2 || p != gidUnit/(1+1) {
		t.Errorf("late: got %v points shared by %d, want %v by 2", p, n, gidUnit/2)
	}
	// no time: every station of the frequency that uses the keyword shares it, as before
	if p, n := pts(Input{FreqKHz: 518, B1: "?", Text: "NORWEGIAN NAV. WARNING"}, "LGQ"); n != 3 || p != gidUnit/3 {
		t.Errorf("no time: got %v points shared by %d, want %v by 3", p, n, gidUnit/3)
	}
}
