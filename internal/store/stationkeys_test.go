package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A database made before station keys existed (version 2, no station_key column) is opened, and its records are given the key of
// their station; after that the record follows its station when the table is reordered.
func TestStationKeysMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Skip("cannot open sqlite directly:", err)
	}
	old := strings.Replace(tableSQL, "batch_id     INTEGER,                            -- the deletion it belongs to\n  station_key  TEXT    NOT NULL DEFAULT ''         -- the station's own identity (frequency, NAVAREA, B1, call sign): what a record really refers to\n", "batch_id     INTEGER\n", 1)
	if old == tableSQL {
		t.Fatal("could not make the old table")
	}
	for _, q := range []string{old, viewsSQL, `PRAGMA user_version = 2`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	rec := `{"id":1,"v":1,"ch":518000,"rx":"` + at.Format(time.RFC3339) + `","end":"` + at.Format(time.RFC3339) + `","kind":"message","b1":"F","area":4,"sid":3,"st":"X","pick":2,"cand":[{"id":3,"label":"X","score":9},{"id":2,"label":"Y","score":4}],"lines":[]}`
	if _, err := db.Exec(`INSERT INTO records(id, received, channel, kind, b1, b2, serial, navarea, station, distance_km, bearing_deg, quality, comment, end_time, rcv, station_id, ident_method, ident_score, manual, picked, version) VALUES (1, ?, 518000, 'message', 'F', 'E', '01', 4, 'X', 0, 0, 0, '', ?, 0, 3, 'UID', 9, 0, 2, 1)`, at.Format(time.RFC3339), at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bodies(id, text, corrected, data) VALUES (1, '', '', ?)`, rec); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	table := []string{"", "k-a", "k-b", "k-c"} // places 1..3
	keyOf := func(id int, label string, ch int) string {
		if id < len(table) {
			return table[id]
		}
		return ""
	}
	n, err := s.BackfillStationKeys(keyOf)
	if err != nil || n != 1 {
		t.Fatalf("backfill: %d %v", n, err)
	}
	if n, _ = s.BackfillStationKeys(keyOf); n != 0 {
		t.Fatalf("a second backfill changed %d", n)
	}
	// the table is reordered: c, b, a
	place := map[string]int{"k-c": 1, "k-b": 2, "k-a": 3}
	s.SetResolver(func(k string) (int, bool) { id, ok := place[k]; return id, ok })
	if moved, err := s.ResyncStations(func(k string) (int, bool) { id, ok := place[k]; return id, ok }); err != nil || moved != 1 {
		t.Fatalf("resync: %d %v", moved, err)
	}
	r, ok := s.Get(1)
	if !ok || r.StationKey != "k-c" || r.StationID != 1 || r.Picked != 2 || len(r.Candidates) != 2 || r.Candidates[0].StationID != 1 || r.Candidates[1].StationID != 2 {
		t.Fatalf("after the reorder: %+v", r)
	}
	if f := (Filter{Unidentified: true}); func() int { _, n := s.Query(f); return n }() != 0 {
		t.Error("the record counts as unidentified")
	}
	// the station is removed from the table: the record names no station, the candidate that is gone is dropped
	delete(place, "k-c")
	if _, err := s.ResyncStations(func(k string) (int, bool) { id, ok := place[k]; return id, ok }); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(1)
	if r.StationID != 0 || len(r.Candidates) != 1 || s.CountUnresolved() != 1 {
		t.Fatalf("after the removal: %+v unresolved %d", r, s.CountUnresolved())
	}
}
