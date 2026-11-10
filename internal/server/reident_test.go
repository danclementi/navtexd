package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"navtex/internal/catalog"
	"navtex/internal/config"
	"navtex/internal/stations"
	"navtex/internal/store"
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

// Imports: a YaND report is placed by label and gets the key; an export of this program follows its keys into a log whose station
// table has another order; an older export (no keys) is placed by label.
func TestImportsFollowStationKeys(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	// log A: the table as shipped
	sa, _, sta, _ := setup(t, cfg)
	adminA := httptest.NewServer(sa.handler(false))
	defer adminA.Close()
	when := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	m := addMsg(t, sta, sa.cat, "F", when, "CCGD5 NOTICE OFFN01 FCST FOR NE US")
	if got, _ := sta.Get(m.ID); !strings.Contains(got.Station, "Boston") {
		t.Fatalf("A: %s", got.Station)
	}
	resp, err := http.Get(adminA.URL + "/api/database/export")
	if err != nil {
		t.Fatal(err)
	}
	export, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(export), `"sk":"518#4#F#NMF"`) {
		t.Fatalf("the export has no key: %.300s", export)
	}
	// log B: the same stations in another order
	sb, _, stb, dirB := setup(t, cfg)
	adminB := httptest.NewServer(sb.handler(false))
	defer adminB.Close()
	path := filepath.Join(dirB, catalog.FileName)
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	rows := lines[1:]
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\r\n"+strings.Join(rows, "\r\n")+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.cat.ReloadFromDisk(); err != nil {
		t.Fatal(err)
	}
	post := func(body []byte, mode string) map[string]any {
		req, _ := http.NewRequest("POST", adminB.URL+"/api/database/import?mode="+mode, bytes.NewReader(body))
		req.Header.Set("X-Navtex", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			t.Fatalf("import: %d %v", resp.StatusCode, out)
		}
		return out
	}
	right := func(tag string) {
		t.Helper()
		recs, _ := stb.Query(store.Filter{})
		if len(recs) != 1 {
			t.Fatalf("%s: %d records", tag, len(recs))
		}
		_, list := sb.cat.Stations()
		r := recs[0]
		if r.StationID < 1 || catalog.Label(list[r.StationID-1]) != r.Station || !strings.Contains(r.Station, "Boston") {
			t.Fatalf("%s: %s at place %d (%v)", tag, r.Station, r.StationID, r.StationKey)
		}
		if n := stb.CountByStation()[r.StationID]; n != 1 {
			t.Fatalf("%s: the count of the station's place is %d", tag, n)
		}
	}
	post(export, "replace")
	right("own export, other order")
	// an older export has no keys: placed by label
	old := strings.ReplaceAll(string(export), `"sk":"518#4#F#NMF",`, "")
	if old == string(export) {
		t.Fatal("could not remove the key")
	}
	post([]byte(old), "replace")
	right("export without keys")
	// a YaND report
	rep := "---\n20261002 0852 518 $04F NMF Boston,USA                   414 [LZ1AQ 1.2m dia.]\n" +
		"2026-10-02 08:52:42 102> ZCZC FA81\n2026-10-02 08:52:47 100> SECNY BNM 0510-26 \n2026-10-02 08:53:27 96> NNNN\n"
	post([]byte(rep), "replace")
	right("YaND report")
	if recs, _ := stb.Query(store.Filter{}); recs[0].StationKey != "518#4#F#NMF" {
		t.Fatalf("YaND record key: %q", recs[0].StationKey)
	}
}

// A message on 4209.5 kHz is identified with a station of that channel (the table takes decimal frequencies).
func TestIdentify4209(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, dir := setup(t, cfg)
	path := filepath.Join(dir, catalog.FileName)
	raw, _ := os.ReadFile(path)
	add := "11#4209.5#C#XVN#Nha Trang#VTN#Viet Nam#12.222#109.181#00:20#04:20#08:20#12:20#16:20#20:20###\r\n" +
		"9#4209.5#X#SUZ#Ismailia#EGY#Egypt#30.467#32.367#07:50#11:50#######\r\n"
	if err := os.WriteFile(path, append(raw, add...), 0o644); err != nil {
		t.Fatal(err)
	}
	if rl, err := s.cat.ReloadFromDisk(); err != nil || rl.Added != 2 {
		t.Fatalf("reload: %+v %v", rl, err)
	}
	at := time.Date(2026, 5, 1, 4, 22, 0, 0, time.UTC)
	for b1, want := range map[string]string{"C": "Nha Trang", "X": "Ismailia"} {
		r := store.Record{Channel: 4209500, Kind: "message", B1: b1, B2: "E", Serial: "01", Received: at, End: at.Add(time.Minute), Quality: 50,
			Lines: []store.Line{{Time: at, Text: "ZCZC " + b1 + "E01"}, {Time: at, Text: "TEST"}, {Time: at, Text: "NNNN"}}}
		s.cat.Enrich(&r)
		saved, err := st.Add(r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(saved.Station, want) || !strings.HasPrefix(saved.StationKey, "4209.5#") {
			t.Errorf("B1 %s on 4209.5: %q key %q", b1, saved.Station, saved.StationKey)
		}
	}
	// the other channels do not pick up these stations
	r := store.Record{Channel: 518000, Kind: "message", B1: "C", Received: at, End: at.Add(time.Minute)}
	s.cat.Enrich(&r)
	if strings.Contains(r.Station, "Nha Trang") {
		t.Errorf("518 kHz got %q", r.Station)
	}
}

// The operator can keep a record unidentified (it stays so when read again) and have it identified again from scratch.
func TestKeepUnidAndIdentifyAgain(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	when := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	sid := 0
	for i := 1; sid == 0; i++ { // the first station of 518 kHz gets a keyword of its own
		if x, ok := s.cat.Station(i); !ok {
			t.Fatal("no 518 kHz station")
		} else if x.Hz() == 518000 {
			sid = i
		}
	}
	x, _ := s.cat.Station(sid)
	if code, _ := do(t, admin, "PUT", "/api/stations/"+strconv.Itoa(sid)+"/keywords", map[string]any{"type": "UID", "keywords": []string{"ZORGLUBX"}}, nil); code != 200 {
		t.Fatal(code)
	}
	r := addMsg(t, st, s.cat, x.ID, when, "ZORGLUBX NOTICE")
	if r.Station == "UNID" {
		t.Fatalf("the keyword did not name %s", x.Name)
	}
	named := r.Station
	code, _ := do(t, admin, "PATCH", "/api/messages/"+strconv.FormatInt(r.ID, 10), map[string]any{"v": r.Version, "station": -1}, nil)
	got, _ := st.Get(r.ID)
	if code != 200 || got.Station != "UNID" || got.Picked != -1 || !got.Manual || got.StationID != 0 {
		t.Fatalf("kept unid: %d %+v", code, got)
	}
	if len(got.Candidates) == 0 {
		t.Error("the candidates should stay for Why?")
	}
	// read again from the database (a restart): still UNID, and the global pass leaves it alone
	again, _ := st.Get(r.ID)
	if again.Station != "UNID" || again.Picked != -1 {
		t.Fatalf("after reading again: %+v", again)
	}
	if res, err := s.cat.ReidentifyUnid(st, 0, false, "test"); err != nil || res.Found != 0 {
		t.Fatalf("global pass: %v %+v", err, res)
	}
	if got, _ := st.Get(r.ID); got.Station != "UNID" {
		t.Fatal("the global pass named a record kept unidentified")
	}
	// identify again from scratch
	code, _ = do(t, admin, "PATCH", "/api/messages/"+strconv.FormatInt(r.ID, 10), map[string]any{"v": got.Version, "reident": true}, nil)
	got, _ = st.Get(r.ID)
	if code != 200 || got.Station != named || got.Picked != 0 || got.Manual {
		t.Fatalf("identify again: %d %+v (wanted %s)", code, got, named)
	}
}
