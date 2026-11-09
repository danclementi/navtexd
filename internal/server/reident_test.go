package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"navtex/internal/catalog"
	"navtex/internal/config"
	"navtex/internal/stations"
)

// New keywords give a station to the messages that had none, in one step that Undo takes back; the rest stays as it was.
func TestIdentifyAgain(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	when := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	a := addMsg(t, st, s.cat, "F", when, "ZORGLUBX NOTICE")
	b := addMsg(t, st, s.cat, "F", when.Add(time.Hour), "ZORGLUBX AGAIN")
	c := addMsg(t, st, s.cat, "F", when.Add(2*time.Hour), "NOTHING USEFUL")
	m := addMsg(t, st, s.cat, "F", when.Add(3*time.Hour), "ZORGLUBX EDITED")
	m.Manual = true
	if _, err := st.Update(m); err != nil {
		t.Fatal(err)
	}
	for _, r := range []int64{a.ID, b.ID, c.ID, m.ID} {
		if got, _ := st.Get(r); got.Station != "UNID" {
			t.Fatalf("record %d starts as %s", r, got.Station)
		}
	}
	// nothing to find yet
	code, j := do(t, admin, "POST", "/api/maintenance/reident", map[string]any{"dry": true}, nil)
	if res := j["result"].(map[string]any); code != 200 || res["found"].(float64) != 0 || res["checked"].(float64) < 3 {
		t.Fatalf("before: %d %v", code, j)
	}
	// the keyword arrives in a station file replaced outside the program
	path := filepath.Join(dir, catalog.FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	done := false
	for i, l := range lines {
		f := strings.Split(strings.TrimRight(l, "\r"), "#")
		if len(f) > 17 && f[1] == "518" && f[3] == "NMF" {
			f[len(f)-2] = strings.TrimPrefix(f[len(f)-2]+",ZORGLUBX", ",")
			lines[i] = strings.Join(f, "#") + strings.TrimPrefix(l, strings.TrimRight(l, "\r"))
			done = true
		}
	}
	if !done {
		t.Fatal("no NMF line in the station file")
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	// (the program still has the old table)
	code, j = do(t, admin, "POST", "/api/maintenance/reident", map[string]any{"dry": true}, nil)
	if j["result"].(map[string]any)["found"].(float64) != 0 {
		t.Fatalf("without reading the file: %v", j)
	}
	code, j = do(t, admin, "POST", "/api/maintenance/reident", map[string]any{"dry": true, "reload": true}, nil)
	res := j["result"].(map[string]any)
	if code != 200 || res["found"].(float64) != 2 || !strings.Contains(res["top"].([]any)[0].(map[string]any)["station"].(string), "Boston") {
		t.Fatalf("dry run: %d %v", code, j)
	}
	if got, _ := st.Get(a.ID); got.Station != "UNID" {
		t.Fatal("the dry run changed a record")
	}
	code, j = do(t, admin, "POST", "/api/maintenance/reident", map[string]any{}, nil)
	if code != 200 || j["backup"] == "" || j["result"].(map[string]any)["found"].(float64) != 2 {
		t.Fatalf("apply: %d %v", code, j)
	}
	for id, want := range map[int64]string{a.ID: "Boston", b.ID: "Boston", c.ID: "UNID", m.ID: "UNID"} {
		if got, _ := st.Get(id); !strings.Contains(got.Station, want) {
			t.Errorf("record %d: %s, want %s", id, got.Station, want)
		}
	}
	if u := j["undo"].(map[string]any); u["available"] != true || u["count"].(float64) != 2 {
		t.Errorf("undo: %v", u)
	}
	// one Undo takes both back
	if code, _ := do(t, admin, "POST", "/api/messages/undo", nil, nil); code != 200 {
		t.Fatalf("undo: %d", code)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if got, _ := st.Get(id); got.Station != "UNID" {
			t.Errorf("after Undo record %d: %s", id, got.Station)
		}
	}
}

// Rows of the station file may be moved, added or removed: the log follows its stations (they are told apart by key, not place).
func TestStationsFollowTheirKeys(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	when := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	a := addMsg(t, st, s.cat, "F", when, "CCGD5 NOTICE OFFN01 FCST FOR NE US") // NMF Boston
	if got, _ := st.Get(a.ID); !strings.Contains(got.Station, "Boston") || got.StationKey == "" {
		t.Fatalf("start: %+v", got)
	}
	_, list := s.cat.Stations()
	nmf := func() int {
		_, l := s.cat.Stations()
		for i, x := range l {
			if x.Callsign == "NMF" && x.FreqKHz == 518 {
				return i + 1
			}
		}
		return 0
	}
	before := nmf()
	path := filepath.Join(dir, catalog.FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	head, rows := lines[0], lines[1:]
	write := func(rs []string) {
		if err := os.WriteFile(path, []byte(head+"\r\n"+strings.Join(rs, "\r\n")+"\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reload := func() map[string]any {
		code, j := do(t, admin, "POST", "/api/maintenance/reident", map[string]any{"dry": true, "reload": true}, nil)
		if code != 200 {
			t.Fatalf("reload: %d %v", code, j)
		}
		return j["reloaded"].(map[string]any)
	}
	station := func() (string, int) {
		g, _ := st.Get(a.ID)
		return g.Station, g.StationID
	}
	// 1. the rows swapped around: the first and the last
	sw := append([]string(nil), rows...)
	sw[0], sw[len(sw)-1] = sw[len(sw)-1], sw[0]
	write(sw)
	if r := reload(); r["changed"] != true || r["added"].(float64) != 0 || r["removed"].(float64) != 0 {
		t.Fatalf("swap: %v", r)
	}
	if name, id := station(); !strings.Contains(name, "Boston") || id != nmf() {
		t.Fatalf("after the swap: %s %d (table says %d)", name, id, nmf())
	}
	// 2. a new station at the top: everything moves down by one, the record keeps its station
	newRow := "1#518#Z#TST#Test Station#TST#Testland#50.000#5.000#00:00#04:00#08:00#12:00#16:00#20:00#KEYWORD##"
	write(append([]string{newRow}, sw...))
	if r := reload(); r["added"].(float64) != 1 || r["removed"].(float64) != 0 {
		t.Fatalf("add: %v", r)
	}
	if name, id := station(); !strings.Contains(name, "Boston") || id != nmf() || id == before {
		t.Fatalf("after adding a station: %s %d (table says %d, was %d)", name, id, nmf(), before)
	}
	// a message that carries the new station's word gets it once the messages are identified again
	b := addMsg(t, st, s.cat, "Z", when.Add(time.Hour), "KEYWORD ONLY")
	if got, _ := st.Get(b.ID); !strings.Contains(got.Station, "Test Station") {
		t.Fatalf("new station: %s", got.Station)
	}
	// 3. the station is deleted from the file: its records keep their label but name no station
	var cut []string
	for _, r := range rows {
		if !strings.Contains(r, "#NMF#") || !strings.Contains(r, "#518#") {
			cut = append(cut, r)
		}
	}
	write(cut)
	r := reload()
	if r["removed"].(float64) < 1 || r["unresolved"].(float64) < 1 {
		t.Fatalf("remove: %v", r)
	}
	if name, id := station(); id != 0 || !strings.Contains(name, "Boston") {
		t.Fatalf("after removing the station: %s %d", name, id)
	}
	// 4. and back: the records find their station again
	write(rows)
	reload()
	if name, id := station(); id != nmf() || id == 0 || !strings.Contains(name, "Boston") {
		t.Fatalf("after putting it back: %s %d", name, id)
	}
	_ = list
}

// Two rows that would have the same key (the same call sign "___" on one frequency, NAVAREA and B1) are told apart by name.
func TestStationKeysTellRowsApart(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, _, _ := setup(t, cfg)
	_, list := s.cat.Stations()
	keys, warns := catalog.StationKeys(list)
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("key %q twice", k)
		}
		seen[k] = true
	}
	if len(warns) != 0 {
		t.Errorf("warnings for the shipped table: %v", warns)
	}
	dup := append(append([]stations.Station(nil), list[0]), list[0])
	if k, w := catalog.StationKeys(dup); k[0] == k[1] || len(w) != 1 {
		t.Errorf("identical rows: %v %v", k, w)
	}
}
