package server

import (
	"bytes"
	"encoding/json"
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
	const head = "YYYYMMDD HHMM KHz ID  CALL STATION,COUNTRY             DIST (Km)"
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
			if i+1 >= len(table) || table[i+1] != strings.Repeat("-", 64) {
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
	if n := strings.Count(string(post("/api/report/text", []int64{b.ID})), head); n != 1 {
		t.Errorf("one group: %d headings", n)
	}
	empty := string(post("/api/report/text", []int64{999}))
	if strings.Contains(empty, "YYYYMMDD") || strings.Contains(empty, "no messages") {
		t.Errorf("empty report: %q", empty)
	}
}
