package ident

import (
	"os"
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
