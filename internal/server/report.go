package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"navtex/internal/store"
)

// The report list: records the operator has moved to the reception report (shift-click on the UTC column, or the
// "Add to report" button in the Log tab). It holds record IDs only and is kept in report.json in the data
// directory, so every browser sees the same list.
var reportMu sync.Mutex

func (s *Server) reportFile() string { return filepath.Join(s.st.Dir(), "report.json") }

func (s *Server) reportIDs() []int64 {
	var d struct {
		IDs []int64 `json:"ids"`
	}
	if b, err := os.ReadFile(s.reportFile()); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	return d.IDs
}

func (s *Server) reportList(w http.ResponseWriter, r *http.Request, id Identity) {
	reportMu.Lock()
	ids := s.reportIDs()
	reportMu.Unlock()
	dist := s.withDist(id)
	out := []recordMsg{}
	for _, n := range ids {
		if rec, ok := s.st.Get(n); ok {
			out = append(out, brief(rec, dist))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Received < out[j].Received })
	writeJSON(w, 200, map[string]any{"records": out})
}

// reportText renders the report list as plain text, in time order. info (the operator's personal information,
// administrators only) goes before or after the messages, or is left out.
func (s *Server) reportText(w http.ResponseWriter, r *http.Request, id Identity) {
	reportMu.Lock()
	ids := s.reportIDs()
	reportMu.Unlock()
	var recs []store.Record
	for _, n := range ids {
		if rec, ok := s.st.Get(n); ok {
			recs = append(recs, rec)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Received.Before(recs[j].Received) })
	info := ""
	if mode := r.URL.Query().Get("info"); id.Role == Admin && (mode == "header" || mode == "footer") {
		info = strings.TrimSpace(s.run.Config().Operator.Info)
	}
	mode := r.URL.Query().Get("info")
	dist := s.withDist(id)
	// NavtexDX layout: a summary table, then "RAW LOG" with every message in full. CR LF line ends, as in the sample.
	var b strings.Builder
	put := func(f string, a ...any) { fmt.Fprintf(&b, f+"\r\n", a...) }
	if info != "" && mode == "header" {
		for _, l := range strings.Split(strings.ReplaceAll(info, "\r", ""), "\n") {
			put("%s", l)
		}
		put("")
	}
	put("YYYYMMDD HHMM KHz ID  CALL STATION,COUNTRY             DIST (Km)")
	put("%s", strings.Repeat("-", 64))
	summary := func(rec store.Record) string {
		call, name := "???", "Unknown,??"
		if st, ok := s.cat.Station(rec.StationID); ok {
			call = st.Callsign
			name = st.Name + "," + st.CountryCode
		}
		b1 := rec.B1
		if b1 == "" {
			b1 = "?"
		}
		line := fmt.Sprintf("%s %-3d $%02d%s %-3s %-28s", rec.Received.UTC().Format("20060102 1504"), rec.Channel, rec.NavArea, b1, call, name)
		if dist && rec.DistanceKm > 0 {
			line += fmt.Sprintf("%4.0f", rec.DistanceKm)
		} else {
			line += "    "
		}
		if rec.Comment != "" {
			return line + " [" + rec.Comment + "]"
		}
		return strings.TrimRight(line, " ")
	}
	for _, rec := range recs {
		put("%s", summary(rec))
	}
	if len(recs) == 0 {
		put("(no messages in the report list)")
	}
	put("")
	put("RAW LOG")
	for _, rec := range recs {
		put("---")
		put("%s", summary(rec))
		text := rec.EffectiveLines()
		if cor := strings.Split(rec.Corrected, "\n"); rec.Corrected != "" && len(cor) == len(text) {
			text = cor // with the autocorrections, shown as [WORD] or [A|B]
		}
		for i, l := range rec.Lines {
			put("%s %02.0f> %s", l.Time.UTC().Format("2006-01-02 15:04:05"), float64(l.Quality), strings.TrimRight(text[i], "\r"))
		}
		put("")
	}
	if info != "" && mode == "footer" {
		put("---")
		for _, l := range strings.Split(strings.ReplaceAll(info, "\r", ""), "\n") {
			put("%s", l)
		}
		put("")
	}
	put("  ")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

func (s *Server) reportEdit(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Add    []int64 `json:"add"`
		Remove []int64 `json:"remove"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	reportMu.Lock()
	have := map[int64]bool{}
	var keep []int64
	for _, n := range s.reportIDs() {
		if _, ok := s.st.Get(n); ok && !have[n] {
			have[n] = true
			keep = append(keep, n)
		}
	}
	for _, n := range body.Remove {
		delete(have, n)
	}
	for _, n := range body.Add {
		if _, ok := s.st.Get(n); ok {
			have[n] = true
		}
	}
	ids := []int64{}
	for _, n := range keep {
		if have[n] {
			ids = append(ids, n)
			delete(have, n)
		}
	}
	for _, n := range body.Add {
		if have[n] {
			ids = append(ids, n)
			delete(have, n)
		}
	}
	b, _ := json.Marshal(map[string]any{"ids": ids})
	err := os.WriteFile(s.reportFile(), b, 0o644)
	reportMu.Unlock()
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.reportList(w, r, id)
}
