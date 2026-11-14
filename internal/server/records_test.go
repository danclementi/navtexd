package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"navtex/internal/config"
	"navtex/internal/sitor"
	"navtex/internal/store"
	"navtex/internal/when"
)

func addMsg(t *testing.T, st *store.Store, cat interface{ Enrich(*store.Record) }, b1 string, at time.Time, lines ...string) store.Record {
	t.Helper()
	r := store.Record{Channel: 518000, Kind: "message", B1: b1, B2: "E", Serial: "01", Received: at, End: at.Add(time.Minute), Quality: 50}
	r.Lines = append(r.Lines, store.Line{Time: at, Text: "ZCZC " + b1 + "E01"})
	for _, l := range lines {
		r.Lines = append(r.Lines, store.Line{Time: at, Text: l})
	}
	r.Lines = append(r.Lines, store.Line{Time: at, Text: "NNNN"})
	cat.Enrich(&r)
	saved, err := st.Add(r)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestRecordsAndStations(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)

	a := addMsg(t, st, s.cat, "F", at, "SOUTH OF NANTUCKET SHOALS", "GALE WARNING")
	if a.Station != "NMF Boston,USA" || a.NavArea != 4 || a.DistanceKm == 0 || a.IdentMethod != "GID" {
		t.Fatalf("identification: %+v", a)
	}
	b := addMsg(t, st, s.cat, "E", at.Add(time.Hour), "NOTHING USEFUL")
	if b.Station != "UNID" {
		t.Fatalf("b identified as %s", b.Station)
	}

	// list: admin sees the distance, a viewer does not
	_, l := do(t, admin, "GET", "/api/messages?order=newest&limit=10", nil, nil)
	rows := l["records"].([]any)
	if len(rows) != 2 || rows[1].(map[string]any)["km"] == nil {
		t.Errorf("admin list: %v", l)
	}
	_, l = do(t, tunnel, "GET", "/api/messages?maxkm=100000", nil, nil)
	for _, r := range l["records"].([]any) {
		if r.(map[string]any)["km"] != nil {
			t.Error("viewer got a distance")
		}
	}
	// distance window: the identified record is somewhere between 0 and 100000 km; unidentified ones never match a bound
	if _, l = do(t, admin, "GET", "/api/messages?minkm=1&maxkm=100000", nil, nil); l["total"].(float64) != 1 {
		t.Errorf("distance window: %v", l["total"])
	}
	// a date bound combined with others: "to" is the whole day
	for q, want := range map[string]float64{"maxkm=100000&to=2026-09-28": 1, "maxkm=100000&to=2026-09-27": 0, "to=2026-09-28": 2, "maxkm=100000&to=2026-09-29&freq=518": 1, "maxkm=100000&to=2026-09-29&freq=490": 0} {
		if _, l = do(t, admin, "GET", "/api/messages?"+q, nil, nil); l["total"].(float64) != want {
			t.Errorf("%s: total %v, want %v", q, l["total"], want)
		}
	}
	if _, l = do(t, admin, "GET", "/api/messages?minkm=100000", nil, nil); l["total"].(float64) != 0 {
		t.Errorf("minkm beyond every record: %v", l["total"])
	}
	if _, l = do(t, admin, "GET", "/api/messages?unid=1", nil, nil); l["total"].(float64) != 1 {
		t.Errorf("unid filter: %v", l["total"])
	}
	if _, l = do(t, admin, "GET", "/api/messages?text=nantucket", nil, nil); l["total"].(float64) != 1 {
		t.Errorf("text filter: %v", l["total"])
	}

	// the operator sets B1 and NAVAREA on the unidentified one
	code, _ := do(t, tunnel, "PATCH", "/api/messages/2", map[string]any{"v": b.Version, "b1": "E", "area": 4}, nil)
	if code != 403 {
		t.Errorf("viewer patch: %d", code)
	}
	code, p := do(t, admin, "PATCH", "/api/messages/2", map[string]any{"v": b.Version, "b1": "E", "area": 4}, nil)
	if code != 200 || p["st"] != "NME Charleston,USA" || p["man"] != true {
		t.Errorf("patch: %d %v", code, p)
	}
	if code, _ = do(t, admin, "PATCH", "/api/messages/2", map[string]any{"v": b.Version, "cmt": "stale"}, nil); code != 409 {
		t.Errorf("stale patch: %d", code)
	}
	if code, _ = do(t, admin, "PATCH", "/api/messages/2", map[string]any{"v": 2, "b1": "9"}, nil); code != 400 {
		t.Errorf("bad B1: %d", code)
	}

	// keywords
	if code, _ = do(t, admin, "PUT", "/api/stations/1/keywords", map[string]any{"type": "GID", "keywords": []string{"alpha", "Alpha", "BETA"}}, nil); code != 200 {
		t.Errorf("keywords: %d", code)
	}
	if st1, _ := s.cat.Station(1); len(st1.GID) != 2 || st1.GID[0] != "ALPHA" {
		t.Errorf("keywords stored: %v", st1.GID)
	}
	long := make([]string, 0)
	for i := 0; i < 40; i++ {
		long = append(long, "KEYWORDNUMBER"+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	if code, _ = do(t, admin, "PUT", "/api/stations/1/keywords", map[string]any{"type": "UID", "keywords": long}, nil); code != 400 {
		t.Errorf("too long keyword list: %d", code)
	}
	if code, _ = do(t, tunnel, "PUT", "/api/stations/1/keywords", map[string]any{"type": "GID", "keywords": []string{"X"}}, nil); code != 403 {
		t.Errorf("viewer keywords: %d", code)
	}

	// deleting several records is one step that can be undone
	if code, m := do(t, admin, "POST", "/api/messages/delete", map[string]any{"ids": []int64{1, 2}}, nil); code != 200 || m["deleted"].(float64) != 2 {
		t.Errorf("delete: %d %v", code, m)
	}
	if code, m := do(t, admin, "GET", "/api/messages/undo", nil, nil); code != 200 || m["available"] != true || m["count"].(float64) != 2 {
		t.Errorf("undo info: %d %v", code, m)
	}
	if code, _ = do(t, tunnel, "POST", "/api/messages/undo", nil, nil); code != 403 {
		t.Errorf("viewer undo: %d", code)
	}
	if code, m := do(t, admin, "POST", "/api/messages/undo", nil, nil); code != 200 || m["restored"].(float64) != 2 {
		t.Errorf("undo: %d %v", code, m)
	}
	if code, m := do(t, admin, "POST", "/api/maintenance/compact", nil, nil); code != 200 || m["backup"] == "" {
		t.Errorf("compact: %d %v", code, m)
	}
	if code, _ = do(t, tunnel, "GET", "/api/database", nil, nil); code != 403 {
		t.Errorf("viewer database info: %d", code)
	}
	if code, _ = do(t, admin, "DELETE", "/api/messages/1", nil, nil); code != 200 {
		t.Errorf("delete one: %d", code)
	}
	if code, _ = do(t, admin, "DELETE", "/api/messages/1", nil, nil); code != 404 {
		t.Errorf("delete missing: %d", code)
	}

	// reverser is open to everybody
	if code, r := do(t, tunnel, "POST", "/api/reverse", map[string]any{"text": "-'54-"}, nil); code != 200 || r["text"] != "ASTRA" {
		t.Errorf("reverse: %d %v", code, r)
	}
}

func TestEphemerisLoggingAndStations(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, _, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()

	// NMF Boston: id 101 in the shipped table
	code, e := do(t, admin, "GET", "/api/ephemeris?station=101&date=2026-09-28", nil, nil)
	if code != 200 {
		t.Fatalf("ephemeris: %d %v", code, e)
	}
	if len(e["local"].([]any)) == 0 || len(e["dx"].([]any)) == 0 || len(e["slots"].([]any)) != 6 {
		t.Errorf("ephemeris content: %v", e)
	}
	first := e["local"].([]any)[0].([]any)
	if a := first[0].(float64); a < 9*60 || a > 12*60 { // sunrise in Norfolk is about 10:50 UTC in late September
		t.Errorf("local sunrise %v minutes", a)
	}
	if code, _ = do(t, tunnel, "GET", "/api/ephemeris?station=101", nil, nil); code != 403 {
		t.Errorf("viewer ephemeris: %d", code)
	}
	if code, _ = do(t, admin, "GET", "/api/ephemeris?station=9999", nil, nil); code != 404 {
		t.Errorf("unknown station: %d", code)
	}

	// stations: distance and bearing for admin only
	_, a := do(t, admin, "GET", "/api/stations", nil, nil)
	_, v := do(t, tunnel, "GET", "/api/stations", nil, nil)
	if a["stations"].([]any)[100].(map[string]any)["km"] == nil {
		t.Error("admin stations without distance")
	}
	if v["stations"].([]any)[100].(map[string]any)["km"] != nil {
		t.Error("viewer stations with distance")
	}

	// selective logging
	if code, _ = do(t, admin, "PUT", "/api/logging", map[string]any{"disabled": []int{101}}, nil); code != 200 {
		t.Errorf("put logging: %d", code)
	}
	if code, _ = do(t, tunnel, "PUT", "/api/logging", map[string]any{"disabled": []int{}}, nil); code != 403 {
		t.Errorf("viewer put logging: %d", code)
	}
	_, l := do(t, tunnel, "GET", "/api/logging", nil, nil)
	if d := l["disabled"].([]any); len(d) != 1 || d[0].(float64) != 101 {
		t.Errorf("logging: %v", l)
	}
}

// recorrect redoes the word autocorrection of every stored record (what a record gets when it arrives) and counts the changes.
func recorrect(t *testing.T, s *Server, st *store.Store) int {
	t.Helper()
	recs, _ := st.Query(store.Filter{})
	n := 0
	for _, rec := range recs {
		old := rec.Corrected
		s.run.Correct(&rec)
		if rec.Corrected != old {
			if _, err := st.Update(rec); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	return n
}

func TestAutocorrectionAndReversalSuggestions(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
	wrong := sitor.Reverse("WARNINGS") // what a word sent in the other shift looks like
	rec := addMsg(t, st, s.cat, "F", at, "STRONG W~NDS AND GALE WARNING", "HEAVY "+wrong+" HERE", "ALL CLEAN")

	// nothing is corrected until the record goes through the runner / maintenance
	_, d := do(t, admin, "GET", "/api/messages/"+fmt.Sprint(rec.ID), nil, nil)
	if d["cor"] != nil {
		t.Errorf("cor before correction: %v", d["cor"])
	}
	rv, _ := d["rv"].([]any)
	if len(rv) != 5 || len(rv[2].([]any)) != 1 {
		t.Fatalf("reversal suggestions per line: %v", d["rv"])
	}
	if got := rv[2].([]any)[0].(map[string]any)["t"]; got != "WARNINGS" {
		t.Errorf("suggested %v", got)
	}

	// the user's own word list extends the vocabulary
	os.WriteFile(filepath.Join(dir, "vocabulary.txt"), []byte("# mine\nWARNINGS\n"), 0o644)
	if n := recorrect(t, s, st); n != 1 {
		t.Fatalf("recorrect: %d changed", n)
	}
	_, d = do(t, admin, "GET", "/api/messages/"+fmt.Sprint(rec.ID), nil, nil)
	cor, _ := d["cor"].(string)
	if !strings.Contains(cor, "STRONG [WINDS] AND GALE WARNING") {
		t.Errorf("corrected text: %q", cor)
	}
	if strings.Count(cor, "\n") != 4 || !strings.HasPrefix(cor, "ZCZC FE01\n") {
		t.Errorf("corrected text must keep the lines: %q", cor)
	}
	// text search finds the corrected word as well as the raw one
	if _, l := do(t, admin, "GET", "/api/messages?text=strong+[winds]", nil, nil); l["total"] != float64(1) {
		t.Errorf("search in corrected text: %v", l["total"])
	}
	if _, l := do(t, admin, "GET", "/api/messages?text=w~nds", nil, nil); l["total"] != float64(1) {
		t.Errorf("search in raw text: %v", l["total"])
	}
	// redoing it changes nothing
	if n := recorrect(t, s, st); n != 0 {
		t.Errorf("second recorrect: %d", n)
	}
	// settings: switch it off, redo, and the corrected text is gone
	do(t, admin, "PUT", "/api/settings", map[string]any{"changes": map[string]any{"autocorrect.enabled": false}}, nil)
	recorrect(t, s, st)
	if _, d = do(t, admin, "GET", "/api/messages/"+fmt.Sprint(rec.ID), nil, nil); d["cor"] != nil {
		t.Errorf("disabled but cor = %v", d["cor"])
	}
}

func TestReportList(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
	a := addMsg(t, st, s.cat, "F", at, "ONE")
	c := addMsg(t, st, s.cat, "F", at.Add(2*time.Hour), "THREE")

	// the list comes from the browser: any user may ask for rows; they are in time order, once each, missing ones dropped
	for name, srv := range map[string]*httptest.Server{"admin": admin, "viewer": tunnel} {
		code, l := do(t, srv, "POST", "/api/report/list", map[string]any{"ids": []int64{c.ID, a.ID, a.ID, 999}}, nil)
		rows, _ := l["records"].([]any)
		if code != 200 || len(rows) != 2 || int64(rows[0].(map[string]any)["id"].(float64)) != a.ID || int64(rows[1].(map[string]any)["id"].(float64)) != c.ID {
			t.Fatalf("%s report rows: %d %v", name, code, l)
		}
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), "report.json")); err == nil {
		t.Error("the server must not keep a report list")
	}
}

func TestRevisionsAndReportText(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.Info = "Operator, test station"
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
	rec := addMsg(t, st, s.cat, "F", at, "-'54-(£-, 4-$89 FORECAST")
	line := 1 // line 0 is the ZCZC line

	patch := func(v int, op map[string]any) (int, map[string]any) {
		return do(t, admin, "PATCH", fmt.Sprintf("/api/messages/%d", rec.ID), map[string]any{"v": v, "rev": op}, nil)
	}
	if code, _ := do(t, tunnel, "PATCH", "/api/messages/1", map[string]any{"v": rec.Version, "rev": map[string]any{"op": "clear"}}, nil); code != 403 {
		t.Errorf("viewer revision: %d", code)
	}
	code, p := patch(rec.Version, map[string]any{"op": "add", "l": line, "find": "-'54-(£-,", "t": "ASTRAKHAN"})
	if code != 200 {
		t.Fatalf("add by text: %d %v", code, p)
	}
	_, d := do(t, admin, "GET", fmt.Sprintf("/api/messages/%d", rec.ID), nil, nil)
	eff := d["eff"].([]any)
	if eff[line] != "<ASTRAKHAN> 4-$89 FORECAST" || d["lines"].([]any)[line].(map[string]any)["x"] != "-'54-(£-, 4-$89 FORECAST" {
		t.Errorf("effective %v raw %v", eff[line], d["lines"].([]any)[line])
	}
	v := int(p["v"].(float64))
	// the same stretch again (overlap), text that is not in the line, and a replacement with the marker characters
	if code, _ = patch(v, map[string]any{"op": "add", "l": line, "find": "54-", "t": "X"}); code != 400 {
		t.Errorf("overlap accepted: %d", code)
	}
	if code, _ = patch(v, map[string]any{"op": "add", "l": line, "find": "NOPE", "t": "X"}); code != 400 {
		t.Errorf("missing text accepted: %d", code)
	}
	if code, _ = patch(v, map[string]any{"op": "add", "l": line, "s": 0, "e": 1, "t": "<A>"}); code != 400 {
		t.Errorf("marker characters accepted: %d", code)
	}
	// the second occurrence is addressed by its number
	rec2 := addMsg(t, st, s.cat, "F", at.Add(time.Hour), "AB AB AB")
	code, _ = do(t, admin, "PATCH", fmt.Sprintf("/api/messages/%d", rec2.ID), map[string]any{"v": rec2.Version, "rev": map[string]any{"op": "add", "l": 1, "find": "AB", "occ": 1, "t": "CD"}}, nil)
	_, d2 := do(t, admin, "GET", fmt.Sprintf("/api/messages/%d", rec2.ID), nil, nil)
	if code != 200 || d2["eff"].([]any)[1] != "AB <CD> AB" {
		t.Errorf("occurrence: %d %v", code, d2["eff"])
	}

	// the report text: personal information as header, as footer, or left out (and never for a viewer)
	get := func(srv *httptest.Server, info string) string {
		body, _ := json.Marshal(map[string]any{"ids": []int64{rec2.ID, rec.ID}, "info": info})
		req, _ := http.NewRequest("POST", srv.URL+"/api/report/text", bytes.NewReader(body))
		req.Header.Set("X-Navtex", "1")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return b.String()
	}
	h, f, o, vtxt := get(admin, "header"), get(admin, "footer"), get(admin, "omit"), get(tunnel, "header")
	if !strings.HasPrefix(h, "Operator, test station\r\n") || strings.Contains(f[:20], "Operator") || !strings.HasSuffix(strings.TrimSpace(f), "Operator, test station") || strings.Contains(o, "Operator, test") || strings.Contains(vtxt, "Operator, test") {
		t.Errorf("info placement:\nheader %q\nfooter %q\nomit %q\nviewer %q", h, f, o, vtxt)
	}
	if !strings.Contains(o, "<ASTRAKHAN> 4-$89 FORECAST") || strings.Index(o, "AB <CD> AB") < strings.Index(o, "<ASTRAKHAN>") {
		t.Errorf("report body (time order, revised text): %q", o)
	}
	// clearing
	_, p = patch(v, map[string]any{"op": "clear"})
	if _, d = do(t, admin, "GET", fmt.Sprintf("/api/messages/%d", rec.ID), nil, nil); d["eff"] != nil || p["rev"] != nil {
		t.Errorf("not cleared: %v", d["eff"])
	}
}

// A viewer sees distances in the report (rows and text) only when the operator allows location information.
func TestReportDistanceForViewers(t *testing.T) {
	for _, public := range []bool{false, true} {
		cfg := config.Default()
		cfg.Input.File.Path = "/nonexistent.wav"
		cfg.Input.Source = "file"
		cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
		cfg.Operator.Public = public
		s, _, st, _ := setup(t, cfg)
		tunnel := httptest.NewServer(s.handler(true))
		a := addMsg(t, st, s.cat, "F", time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC), "SOUTH OF NANTUCKET SHOALS", "GALE WARNING")
		if a.DistanceKm == 0 {
			t.Fatal("test record has no distance")
		}
		_, l := do(t, tunnel, "POST", "/api/report/list", map[string]any{"ids": []int64{a.ID}}, nil)
		km, _ := l["records"].([]any)[0].(map[string]any)["km"].(float64)
		body, _ := json.Marshal(map[string]any{"ids": []int64{a.ID}})
		req, _ := http.NewRequest("POST", tunnel.URL+"/api/report/text", bytes.NewReader(body))
		req.Header.Set("X-Navtex", "1")
		resp, err := tunnel.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		buf := new(strings.Builder)
		b := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(b)
			buf.Write(b[:n])
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		tunnel.Close()
		has := strings.Contains(buf.String(), fmt.Sprintf("%.0f", a.DistanceKm))
		if public != (km > 0) || public != has {
			t.Errorf("public=%v: viewer rows km=%v, text has distance=%v", public, km, has)
		}
	}
}

// Two stations can share frequency, B1 and NAVAREA (518 kHz C / NAVAREA 4: Moisie and Santa Marta). Choosing one of the
// candidates must then pick that station, not just set B1 and NAVAREA, which leave the choice open.
func TestPickCandidateStation(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	at := time.Date(2026, 9, 28, 3, 20, 0, 0, time.UTC)
	rec := addMsg(t, st, s.cat, "C", at, "NOTHING USEFUL")
	if rec.Station != "UNID" || len(rec.Candidates) < 2 {
		t.Fatalf("expected an undecided record with candidates: %q %v", rec.Station, rec.Candidates)
	}
	var moisie, marta int
	for _, c := range rec.Candidates {
		switch {
		case strings.Contains(c.Label, "Moisie"):
			moisie = c.StationID
		case strings.Contains(c.Label, "Santa Marta"):
			marta = c.StationID
		}
	}
	if moisie == 0 || marta == 0 {
		t.Fatalf("candidates: %v", rec.Candidates)
	}
	url := fmt.Sprintf("/api/messages/%d", rec.ID)
	// B1 and NAVAREA alone cannot decide
	code, p := do(t, admin, "PATCH", url, map[string]any{"v": rec.Version, "b1": "C", "area": 4}, nil)
	if code != 200 || p["st"] != "UNID" {
		t.Fatalf("B1 and area only: %d %v", code, p["st"])
	}
	v := int(p["v"].(float64))
	code, p = do(t, admin, "PATCH", url, map[string]any{"v": v, "station": marta}, nil)
	if code != 200 || !strings.Contains(p["st"].(string), "Santa Marta") || p["man"] != true || p["im"] != "manual" {
		t.Fatalf("pick Santa Marta: %d %v %v", code, p["st"], p["im"])
	}
	// re-identification leaves the choice alone
	if _, err := s.cat.Reidentify(st); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Get(rec.ID)
	if !strings.Contains(got.Station, "Santa Marta") {
		t.Fatalf("after re-identification: %s", got.Station)
	}
	// the other one, then back to undecided by editing B1
	code, p = do(t, admin, "PATCH", url, map[string]any{"v": got.Version, "station": moisie}, nil)
	if code != 200 || !strings.Contains(p["st"].(string), "Moisie") {
		t.Fatalf("pick Moisie: %d %v", code, p["st"])
	}
	v = int(p["v"].(float64))
	code, p = do(t, admin, "PATCH", url, map[string]any{"v": v, "area": 4}, nil)
	if code != 200 || p["st"] != "UNID" {
		t.Fatalf("editing NAVAREA drops the pick: %d %v", code, p["st"])
	}
	// a station on another frequency is refused
	v = int(p["v"].(float64))
	if code, _ = do(t, admin, "PATCH", url, map[string]any{"v": v, "station": 1}, nil); code != 400 {
		t.Fatalf("station on another frequency: %d", code)
	}
}

// Any record can be given a station by the operator, also a fragment: the identification is marked "forced ID".
func TestForcedIDOnFragment(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	rec := addMsg(t, st, s.cat, "C", time.Date(2026, 9, 28, 3, 20, 0, 0, time.UTC), "NOTHING USEFUL")
	if len(rec.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	rec.Kind, rec.B1 = "fragment", ""
	rec, err := st.Update(rec)
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/api/messages/%d", rec.ID)
	code, p := do(t, admin, "PATCH", url, map[string]any{"v": rec.Version, "station": rec.Candidates[0].StationID}, nil)
	if code != 200 || p["im"] != "forced ID" || p["sid"] == float64(0) || p["kind"] != "fragment" {
		t.Fatalf("forced ID on a fragment: %d im=%v sid=%v", code, p["im"], p["sid"])
	}
}

func TestStationTableImportKeepsBackup(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, _, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	resp, err := http.Get(admin.URL + "/api/stations/export")
	if err != nil {
		t.Fatal(err)
	}
	table, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	post := func(body []byte) (int, map[string]any) {
		req, _ := http.NewRequest("POST", admin.URL+"/api/stations/import", bytes.NewReader(body))
		req.Header.Set("X-Navtex", "1")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		return r.StatusCode, m
	}
	if code, m := post([]byte("this is not a station table")); code != 400 {
		t.Errorf("garbage: %d %v", code, m)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "backups", "navtexd_stations-*")); len(m) != 0 {
		t.Errorf("a refused file must not leave a backup: %v", m)
	}
	code, m := post(table)
	if code != 200 || m["stations"].(float64) < 1 || m["backup"] == "" {
		t.Fatalf("import: %d %v", code, m)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "backups", m["backup"].(string))); len(got) == 0 {
		t.Error("the previous table is not in the backups folder")
	}
}

func TestStationTableMergeImport(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, _, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	post := func(mode, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", admin.URL+"/api/stations/import?mode="+mode, strings.NewReader(body))
		req.Header.Set("X-Navtex", "1")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		return r.StatusCode, m
	}
	before, _ := os.ReadFile(filepath.Join(dir, "navtexd_stations.txt"))
	file := "VERSION5\r\n1#490#A#EJM#Malin Head for NW app UK#IRL#Ireland#55.363#-7.340#00:00#04:00#08:00#12:00#16:00#20:00#MYNEWWORD##\r\n" +
		"1#490#Z#TST#Test#IRL#Ireland#50#-7#00:50#04:50#08:50#12:50#16:50#20:50#TESTPLACE##\r\n"
	code, m := post("merge", file)
	if code != 200 || m["added"].(float64) != 1 || m["changed"].(float64) != 1 || m["keywords"].(float64) != 1 || m["backup"] == "" {
		t.Fatalf("merge: %d %v", code, m)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "navtexd_stations.txt"))
	if !strings.Contains(string(after), "MYNEWWORD") || !strings.Contains(string(after), "#TST#Test#") || len(after) <= len(before) {
		t.Error("the merge is not in the table file")
	}
	if code, m := post("merge", file); code != 200 || m["added"].(float64) != 0 || m["changed"].(float64) != 0 || m["backup"] != "" {
		t.Errorf("merging again: %d %v", code, m)
	}
	if code, _ := post("nonsense", file); code != 400 {
		t.Errorf("unknown mode: %d", code)
	}
}

// From / To take a date or a date and minute; To includes the whole day or the whole minute named.
func TestLogFilterTimes(t *testing.T) {
	for _, c := range []struct{ to, want string }{
		{"2026-09-29", "2026-09-30T00:00:00Z"},
		{"2026-09-29T00:40", "2026-09-29T00:41:00Z"},
	} {
		f, err := logFilter(url.Values{"from": {"2026-09-29T00:10"}, "to": {c.to}}, false)
		if err != nil || f.To.UTC().Format(time.RFC3339) != c.want || f.From.UTC().Format(time.RFC3339) != "2026-09-29T00:10:00Z" {
			t.Errorf("to=%s: %v %v %v", c.to, f.From, f.To, err)
		}
	}
}

// A From or To that starts "local:" is in the receiver's zone and may be relative; "today" is the receiver's today.
func TestLogFilterLocalTimes(t *testing.T) {
	loc, err := when.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	z := when.Zone{Loc: loc, Lat: 40.71, Lon: -74.0, HavePos: true}
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC) // 22:00 on the 12th in New York
	ts := func(f, to string) (string, string, error) {
		fl, err := logFilterZ(url.Values{"from": {f}, "to": {to}}, false, z, now)
		return fl.From.UTC().Format("2006-01-02T15:04Z"), fl.To.UTC().Format("2006-01-02T15:04Z"), err
	}
	// yesterday and today, whole days: the 11th 00:00 EST to the end of the 12th
	if f, to, err := ts("local:today-1", "local:today"); err != nil || f != "2026-11-11T05:00Z" || to != "2026-11-13T05:00Z" {
		t.Errorf("days: %s %s %v", f, to, err)
	}
	// a fixed local date and time, To including its minute
	if f, to, err := ts("local:2026-11-10@08:00", "local:2026-11-10@09:30"); err != nil || f != "2026-11-10T13:00Z" || to != "2026-11-10T14:31Z" {
		t.Errorf("fixed: %s %s %v", f, to, err)
	}
	// the evening before to an hour after sunrise: sunset on the 11th is 16:40 EST, sunrise on the 12th 06:36 EST
	f, to, err := ts("local:today-1@sunset-1h", "local:today@sunrise+1h")
	if err != nil || f[:11] != "2026-11-11T" || f[11:13] != "20" || to[:11] != "2026-11-12T" || to[11:13] != "12" {
		t.Errorf("last night: %s %s %v", f, to, err)
	}
	// UTC values are unchanged
	if f, to, err := ts("2026-11-10", "2026-11-10"); err != nil || f != "2026-11-10T00:00Z" || to != "2026-11-11T00:00Z" {
		t.Errorf("utc: %s %s %v", f, to, err)
	}
	// mistakes and a missing sun are reported
	for _, bad := range []string{"local:today-9", "local:today@noon", "local:tomorrow", "local:2026-11-10@25:00"} {
		if _, _, err := ts(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	z.HavePos = false
	if _, _, err := ts("local:today@sunset", ""); err == nil || !strings.Contains(err.Error(), "position") {
		t.Errorf("sun without a position: %v", err)
	}
	z.HavePos, z.Lat, z.Lon = true, 69.65, 18.96
	if _, _, err := ts("local:2026-06-21@sunset", ""); err == nil || !strings.Contains(err.Error(), "does not rise or set") {
		t.Errorf("midnight sun: %v", err)
	}
}

// /api/when describes the zone and resolves a value in both zones (the filter dialog's boxes).
func TestWhenEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.TimeZone = "America/New_York"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 40.71, -74.0
	s, _, _, _ := setup(t, cfg)
	srv := httptest.NewServer(s.handler(false))
	defer srv.Close()
	tunnel := httptest.NewServer(s.handler(true)) // viewers may use it too
	defer tunnel.Close()
	code, j := do(t, tunnel, "GET", "/api/when", nil, nil)
	if code != 200 || j["zone"] != "America/New_York" || j["sun"] != true {
		t.Fatalf("zone: %d %v", code, j)
	}
	// a fixed value moves between the zones: 2026-11-10 08:00 local (EST) is 13:00 UTC
	code, j = do(t, srv, "GET", "/api/when?v=local:2026-11-10@08:00", nil, nil)
	if code != 200 || j["utc_date"] != "2026-11-10" || j["utc_time"] != "13:00" || j["local_time"] != "08:00" || j["has_time"] != true {
		t.Errorf("local to utc: %d %v", code, j)
	}
	// 2026-11-10 02:30 UTC is the evening of the 9th in New York
	code, j = do(t, srv, "GET", "/api/when?v=utc:2026-11-10T02:30", nil, nil)
	if code != 200 || j["local_date"] != "2026-11-09" || j["local_time"] != "21:30" {
		t.Errorf("utc to local: %d %v", code, j)
	}
	// a relative value resolves now; a date alone has no time part
	code, j = do(t, srv, "GET", "/api/when?v=local:today-1", nil, nil)
	if want := time.Now().In(s.zone().Loc).AddDate(0, 0, -1).Format("2006-01-02"); code != 200 || j["local_date"] != want || j["has_time"] != false {
		t.Errorf("today-1: want %s: %d %v", want, code, j)
	}
	if code, j = do(t, srv, "GET", "/api/when?v=local:today@sunset-1h", nil, nil); code != 200 || j["local_time"] == "" {
		t.Errorf("sunset: %d %v", code, j)
	}
	if code, _ = do(t, srv, "GET", "/api/when?v=local:today-9", nil, nil); code != 400 {
		t.Errorf("a bad value: %d", code)
	}
}

// The trash can be listed (the latest deletion first), searched, and any of its messages restored, whichever deletion they
// were in; viewers may not touch it.
func TestTrashListAndRestore(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
	a := addMsg(t, st, s.cat, "E", at, "GALE WARNING NORTH")
	b := addMsg(t, st, s.cat, "E", at.Add(time.Hour), "ICE REPORT")
	c := addMsg(t, st, s.cat, "E", at.Add(2*time.Hour), "FOG IN THE CHANNEL")
	do(t, admin, "POST", "/api/messages/delete", map[string]any{"ids": []int64{a.ID}}, nil)
	do(t, admin, "POST", "/api/messages/delete", map[string]any{"ids": []int64{b.ID, c.ID}}, nil)

	code, l := do(t, admin, "GET", "/api/trash", nil, nil)
	rows, _ := l["records"].([]any)
	if code != 200 || l["total"].(float64) != 3 || len(rows) != 3 {
		t.Fatalf("trash list: %d %v", code, l)
	}
	if first := rows[0].(map[string]any); first["id"].(float64) == float64(a.ID) || first["hex"] == "" || first["text"] == "" {
		t.Errorf("the latest deletion comes first: %v", first)
	}
	if _, l = do(t, admin, "GET", "/api/trash?text=gale", nil, nil); l["total"].(float64) != 1 {
		t.Errorf("search in the trash: %v", l)
	}
	if code, _ = do(t, tunnel, "GET", "/api/trash", nil, nil); code != 403 && code != 401 {
		t.Errorf("a viewer must not list the trash: %d", code)
	}
	if code, _ = do(t, tunnel, "POST", "/api/trash/restore", map[string]any{"ids": []int64{a.ID}}, nil); code != 403 && code != 401 {
		t.Errorf("a viewer must not restore: %d", code)
	}
	// one message out of the latest deletion, and one of the first
	code, r := do(t, admin, "POST", "/api/trash/restore", map[string]any{"ids": []int64{a.ID, c.ID, 9999}}, nil)
	if code != 200 || r["restored"].(float64) != 2 {
		t.Fatalf("restore: %d %v", code, r)
	}
	if st.Count() != 2 {
		t.Errorf("count %d", st.Count())
	}
	if _, l = do(t, admin, "GET", "/api/trash", nil, nil); l["total"].(float64) != 1 {
		t.Errorf("trash after restore: %v", l)
	}
	if code, _ = do(t, admin, "POST", "/api/trash/restore", map[string]any{"ids": []int64{a.ID}}, nil); code != 404 {
		t.Errorf("restoring what is not in the trash: %d", code)
	}
	if code, _ = do(t, admin, "POST", "/api/trash/restore", map[string]any{"ids": []int64{}}, nil); code != 400 {
		t.Errorf("no ids: %d", code)
	}
}

// A selection over several lines is put in as several revisions in one step: all or none, and one Undo.
func TestRevisionsOverSeveralLines(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
	rec := addMsg(t, st, s.cat, "F", at, "AB CD", "EF GH")
	url := fmt.Sprintf("/api/messages/%d", rec.ID)
	bad := []map[string]any{{"op": "add", "l": 1, "find": "AB", "t": "12"}, {"op": "add", "l": 2, "find": "NOPE", "t": "34"}}
	if code, _ := do(t, admin, "PATCH", url, map[string]any{"v": rec.Version, "revs": bad}, nil); code != 400 {
		t.Fatalf("a bad revision among several was accepted: %d", code)
	}
	if _, d := do(t, admin, "GET", url, nil, nil); d["eff"] != nil {
		t.Fatalf("the first revision was kept although the second failed: %v", d["eff"])
	}
	good := []map[string]any{{"op": "add", "l": 1, "find": "CD", "t": "34"}, {"op": "add", "l": 2, "find": "EF", "t": "56"}}
	if code, p := do(t, admin, "PATCH", url, map[string]any{"v": rec.Version, "revs": good}, nil); code != 200 {
		t.Fatalf("revisions over two lines: %d %v", code, p)
	}
	_, d := do(t, admin, "GET", url, nil, nil)
	if eff := d["eff"].([]any); eff[1] != "AB <34>" || eff[2] != "<56> GH" {
		t.Errorf("effective text %v", eff)
	}
}

// The pound sign is a NAVTEX character (the reverser makes it of an H): a revision may hold it; other non-ASCII may not.
func TestRevisionMayHoldThePoundSign(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	rec := addMsg(t, st, s.cat, "F", time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC), "WEATHER HIGH")
	url := fmt.Sprintf("/api/messages/%d", rec.ID)
	if code, _ := do(t, admin, "PATCH", url, map[string]any{"v": rec.Version, "rev": map[string]any{"op": "add", "l": 1, "find": "HIGH", "t": "é"}}, nil); code != 400 {
		t.Errorf("other non-ASCII accepted: %d", code)
	}
	code, p := do(t, admin, "PATCH", url, map[string]any{"v": rec.Version, "rev": map[string]any{"op": "add", "l": 1, "find": "HIGH", "t": "£IG£"}}, nil)
	if code != 200 {
		t.Fatalf("pound sign refused: %d %v", code, p)
	}
	if _, d := do(t, admin, "GET", url, nil, nil); d["eff"].([]any)[1] != "WEATHER <£IG£>" {
		t.Errorf("effective %v", d["eff"])
	}
}

// The numbered filter presets: viewers read them, only an administrator stores or clears one, and they outlive the program.
func TestFilterPresets(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, _, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	code, j := do(t, tunnel, "GET", "/api/presets", nil, nil)
	if sl, _ := j["slots"].([]any); code != 200 || len(sl) != 10 || sl[0] != nil {
		t.Fatalf("empty list: %d %v", code, j)
	}
	f := map[string]any{"version": 1, "zone": "local", "from": map[string]any{"d": "today-1", "s": "sunset-1h"}, "to": map[string]any{"d": "today", "s": "sunrise+1h"}, "freq": "518", "kind": "message", "name": "Night"}
	if code, _ = do(t, tunnel, "PUT", "/api/presets/2", f, nil); code != 403 && code != 401 {
		t.Errorf("a viewer stored a preset: %d", code)
	}
	if code, _ = do(t, tunnel, "DELETE", "/api/presets/2", nil, nil); code != 403 && code != 401 {
		t.Errorf("a viewer cleared a preset: %d", code)
	}
	if code, j = do(t, admin, "PUT", "/api/presets/2", f, nil); code != 200 {
		t.Fatalf("store: %d %v", code, j)
	}
	// the relative words are kept, and a viewer sees them
	_, j = do(t, tunnel, "GET", "/api/presets", nil, nil)
	two, _ := j["slots"].([]any)[1].(map[string]any)
	if from, _ := two["from"].(map[string]any); two == nil || from["d"] != "today-1" || from["s"] != "sunset-1h" || from["date"] != "" || two["freq"] != "518" || two["name"] != "Night" {
		t.Errorf("stored: %v", two)
	}
	if _, err := os.Stat(filepath.Join(dir, "navtexd_filter_presets.json")); err != nil {
		t.Errorf("no preset file: %v", err)
	}
	for name, body := range map[string]any{
		"unknown field":   map[string]any{"version": 1, "zone": "utc", "evil": 1},
		"bad relative":    map[string]any{"version": 1, "zone": "local", "from": map[string]any{"d": "today-9"}},
		"relative in utc": map[string]any{"version": 1, "zone": "utc", "from": map[string]any{"d": "today"}},
	} {
		if code, _ = do(t, admin, "PUT", "/api/presets/3", body, nil); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ = do(t, admin, "PUT", "/api/presets/11", f, nil); code != 404 {
		t.Errorf("slot 11: %d", code)
	}
	if code, j = do(t, admin, "PUT", "/api/presets/4", f, nil); code != 400 || !strings.Contains(fmt.Sprint(j["error"]), "preset 2") {
		t.Errorf("a name used twice: %d %v", code, j)
	}
	if code, _ = do(t, admin, "DELETE", "/api/presets/2", nil, nil); code != 200 {
		t.Errorf("clear: %d", code)
	}
	if _, j = do(t, tunnel, "GET", "/api/presets", nil, nil); j["slots"].([]any)[1] != nil {
		t.Errorf("not cleared: %v", j)
	}
}
