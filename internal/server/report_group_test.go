package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"navtex/internal/config"
	"navtex/internal/store"
)

// addMsgAt is addMsg for a given channel frequency (Hz).
func addMsgAt(t *testing.T, s *Server, st *store.Store, hz int, at time.Time, text string) store.Record {
	t.Helper()
	r := store.Record{Channel: hz, Kind: "message", B1: "F", B2: "E", Serial: "01", Received: at, End: at.Add(time.Minute), Quality: 50}
	r.Lines = append(r.Lines, store.Line{Time: at, Text: "ZCZC FE01"}, store.Line{Time: at, Text: text}, store.Line{Time: at, Text: "NNNN"})
	s.cat.Enrich(&r)
	saved, err := st.Add(r)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// The report is grouped by frequency (518, 490, 486, 424, 4209.5 kHz) and sorted by time within a group; every group that has
// messages starts with the column heading, a frequency without messages has none, and an empty report has no heading at all.
func TestReportGroupsByFrequency(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	srv := httptest.NewServer(s.handler(false))
	defer srv.Close()
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return day.Add(time.Duration(h) * time.Hour) }
	a := addMsgAt(t, s, st, 518000, at(10), "A518LATE")
	b := addMsgAt(t, s, st, 490000, at(9), "B490")
	c := addMsgAt(t, s, st, 518000, at(8), "C518EARLY")
	d := addMsgAt(t, s, st, 4209500, at(7), "D4209")
	e := addMsgAt(t, s, st, 486000, at(11), "E486")

	post := func(path string, ids []int64) []byte {
		body, _ := json.Marshal(map[string]any{"ids": ids})
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(body))
		req.Header.Set("X-Navtex", "1")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return out
	}

	// the table in the Report tab: the same order
	var l struct {
		Records []struct {
			ID int64 `json:"id"`
		} `json:"records"`
	}
	if err := json.Unmarshal(post("/api/report/list", []int64{a.ID, b.ID, c.ID, d.ID, e.ID}), &l); err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, r := range l.Records {
		got = append(got, r.ID)
	}
	want := []int64{c.ID, a.ID, b.ID, e.ID, d.ID}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows %v, want %v", got, want)
		}
	}

	// the text: 518 (early, late), 490, 486, 4209.5; one heading per group, the groups apart by a blank line
	txt := string(post("/api/report/text", []int64{a.ID, b.ID, c.ID, d.ID, e.ID}))
	lines := strings.Split(strings.ReplaceAll(txt, "\r", ""), "\n")
	head := reportHeader(6, 3) // 4209.5 kHz is in the report, so the KHz column is 6 wide
	var table []string
	for _, ln := range lines {
		if ln == "RAW LOG" {
			break
		}
		table = append(table, ln)
	}
	var kinds []string
	for i, ln := range table {
		switch {
		case ln == head:
			kinds = append(kinds, "H")
			if i+1 >= len(table) || table[i+1] != strings.Repeat("-", len(head)) {
				t.Errorf("heading without its rule line at %d", i)
			}
			if i > 0 && table[i-1] != "" {
				t.Errorf("no blank line before the heading at %d", i)
			}
		case strings.HasPrefix(ln, "20261115 "):
			kinds = append(kinds, strings.Fields(ln)[2])
		}
	}
	if g := strings.Join(kinds, " "); g != "H 518 518 H 490 H 486 H 4209.5" {
		t.Errorf("table order %q\n%s", g, txt)
	}
	if strings.Index(txt, "C518EARLY") > strings.Index(txt, "A518LATE") || strings.Index(txt, "A518LATE") > strings.Index(txt, "B490") || strings.Index(txt, "B490") > strings.Index(txt, "E486") || strings.Index(txt, "E486") > strings.Index(txt, "D4209") {
		t.Errorf("raw log order:\n%s", txt)
	}
	if strings.Contains(txt, "no messages") {
		t.Errorf("the 'no messages' line is gone:\n%s", txt)
	}

	// one group only: one heading; nothing in the report: no heading, no 'no messages' line
	if n := strings.Count(string(post("/api/report/text", []int64{b.ID})), reportHeader(3, 3)); n != 1 { // no 4209.5 here: the KHz column is 3 wide
		t.Errorf("one group: %d headings", n)
	}
	empty := string(post("/api/report/text", []int64{999}))
	if strings.Contains(empty, "YYYYMMDD") || strings.Contains(empty, "no messages") {
		t.Errorf("empty report: %q", empty)
	}
}

// The columns line up (in a monospaced font): the heading is the one of the sample, the rule is as wide, every row with a distance is
// as wide, the station starts under STATION and the distance ends under the "m" of "(Km)".
func TestReportColumns(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	srv := httptest.NewServer(s.handler(false))
	defer srv.Close()
	at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
	a := addMsg(t, st, s.cat, "F", at, "SOUTH OF NANTUCKET SHOALS", "GALE WARNING")
	if a.DistanceKm == 0 {
		t.Fatal("test record has no distance")
	}
	body, _ := json.Marshal(map[string]any{"ids": []int64{a.ID}})
	req, _ := http.NewRequest("POST", srv.URL+"/api/report/text", bytes.NewReader(body))
	req.Header.Set("X-Navtex", "1")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	lines := strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n")
	const sample = "YYYYMMDD HHMM KHz ID  CALL STATION,COUNTRY                  DIST (Km)"
	if lines[0] != sample {
		t.Errorf("heading %q\nwant    %q", lines[0], sample)
	}
	if lines[1] != strings.Repeat("-", 69) {
		t.Errorf("rule %q", lines[1])
	}
	row := lines[2]
	if len(row) != 69 || !strings.HasPrefix(row, "20260928 0050 518 $") {
		t.Fatalf("row %q (%d)", row, len(row))
	}
	if strings.TrimSpace(row[60:]) != fmt.Sprintf("%.0f", a.DistanceKm) || row[60] != ' ' || row[68] == ' ' {
		t.Errorf("distance not right-aligned under DIST (Km): %q", row[60:])
	}
	if row[27] == ' ' || row[26] != ' ' {
		t.Errorf("station does not start under STATION: %q", row)
	}
}

func TestFitStation(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Odesa,UKR", "Odesa,UKR"},
		{"Porto Santo (CENCOMARMADEIRA),MDR", "Porto Santo (CENCOMARMADEIRA),MDR"},
		{"Porto Santo (CENCOMARMADEIRA EXTRA),MDR", "Porto Santo (CENCOMARMADEIRA,MDR"},
		{"Zürich Äußere Hafenanlage Süd Kai,CHE", "Zürich Äußere Hafenanlage Süd,CHE"},
		{"NoCountryNoCountryNoCountryNoCountryNoCountry", "NoCountryNoCountryNoCountryNoCoun"},
	} {
		got := fitStation(c.in, 33)
		if got != c.want || len([]rune(got)) > 33 {
			t.Errorf("fitStation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// KHz and CALL are as wide as the longest value in the report, so a long callsign (DDH49, CTV-251) or 4209.5 kHz moves the
// station, distance and comment columns of every row and of the heading by the same amount.
func TestReportRowsAlign(t *testing.T) {
	at := time.Date(2026, 11, 16, 19, 50, 0, 0, time.UTC)
	rows := []reportRow{
		{when: at, khz: "490", id: "$03X", call: "UTT", station: "Odesa,UKR", dist: "2483"},
		{when: at, khz: "518", id: "$01A", call: "DDH49", station: "Pinneberg,DEU", dist: "6021"},
		{when: at, khz: "518", id: "$02B", call: "CTV-251", station: "Penalva (CENCOMAR),POR", dist: "5001", comment: "note"},
		{when: at, khz: "4209.5", id: "$01F", call: "???", station: "Unknown,??"},
	}
	k, c := reportWidths(rows)
	if k != 6 || c != 7 {
		t.Fatalf("widths %d %d", k, c)
	}
	head := reportHeader(k, c)
	col := strings.Index(head, "STATION")
	end := len(head)
	for _, r := range rows {
		line := r.text(k, c)
		if line[col-1] != ' ' || line[col] == ' ' {
			t.Errorf("station not under STATION:\n%s\n%s", head, line)
		}
		if r.dist != "" && (len(line) < end || strings.TrimRight(line[:end], " ")[end-1] == ' ' || !strings.HasSuffix(strings.TrimRight(line[:end], " "), r.dist)) {
			t.Errorf("distance not right-aligned:\n%s\n%s", head, line)
		}
	}
	// three-letter callsigns and no 4209.5: the heading of the sample
	k, c = reportWidths(rows[:1])
	if reportHeader(k, c) != "YYYYMMDD HHMM KHz ID  CALL STATION,COUNTRY                  DIST (Km)" {
		t.Errorf("sample heading %q", reportHeader(k, c))
	}
	// a callsign longer than the limit is cut
	if _, c := reportWidths([]reportRow{{call: "ABCDEFGHIJKLMNOP"}}); c != reportMaxCall {
		t.Errorf("callsign limit %d", c)
	}
}

// "include comments": the comment of a record is in the report unless the request says false (a request without the field,
// as from an older page, includes it).
func TestReportComments(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	srv := httptest.NewServer(s.handler(false))
	defer srv.Close()
	at := time.Date(2026, 11, 16, 19, 50, 0, 0, time.UTC)
	r := store.Record{Channel: 518000, Kind: "message", B1: "F", B2: "E", Serial: "01", Received: at, End: at.Add(time.Minute), Quality: 50, Comment: "active vertical dipole"}
	r.Lines = append(r.Lines, store.Line{Time: at, Text: "ZCZC FE01"}, store.Line{Time: at, Text: "TEXT"}, store.Line{Time: at, Text: "NNNN"})
	s.cat.Enrich(&r)
	rec, err := st.Add(r)
	if err != nil {
		t.Fatal(err)
	}
	text := func(extra map[string]any) string {
		req := map[string]any{"ids": []int64{rec.ID}}
		for k, v := range extra {
			req[k] = v
		}
		body, _ := json.Marshal(req)
		hr, _ := http.NewRequest("POST", srv.URL+"/api/report/text", bytes.NewReader(body))
		hr.Header.Set("X-Navtex", "1")
		resp, err := srv.Client().Do(hr)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return string(out)
	}
	for name, c := range map[string]struct {
		extra map[string]any
		want  bool
	}{"absent": {nil, true}, "true": {map[string]any{"comments": true}, true}, "false": {map[string]any{"comments": false}, false}} {
		got := text(c.extra)
		if has := strings.Contains(got, "[active vertical dipole]"); has != c.want {
			t.Errorf("%s: comment in report = %v, want %v\n%s", name, has, c.want, got)
		}
		if n := strings.Count(got, "[active vertical dipole]"); c.want && n != 2 { // the table and the raw log
			t.Errorf("%s: %d times", name, n)
		}
	}
}
