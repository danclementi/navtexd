package store

import "testing"

func TestFilterSets(t *testing.T) {
	a := &Record{NavArea: 4, B1: "E", StationID: 7}
	u := &Record{}
	f := Filter{NavAreas: map[int]bool{4: true, 5: true}, B1s: map[string]bool{"E": true}}
	if !f.match(a) || f.match(u) {
		t.Error("NAVAREA / ID letter sets")
	}
	if !(Filter{OmitUnid: true}).match(a) || (Filter{OmitUnid: true}).match(u) {
		t.Error("OmitUnid")
	}
	if !(Filter{}).match(u) {
		t.Error("empty filter matches everything")
	}
}
