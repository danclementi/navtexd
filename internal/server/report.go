package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"navtex/internal/store"
)

// The report: records a user has collected for a reception report. The list of record IDs lives in the user's own
// browser (every user, administrator or viewer, has their own); the server only turns a list of IDs into rows or text,
// applying the same privacy rules as everywhere else (a viewer gets no distances and no personal information).

type reportReq struct {
	IDs  []int64 `json:"ids"`
	Info string  `json:"info"`
}

// reportRecords returns the stored records for ids, once each and in time order; IDs that no longer exist are dropped.
func (s *Server) reportRecords(ids []int64) []store.Record {
	seen := map[int64]bool{}
	var recs []store.Record
	for _, n := range ids {
		if seen[n] {
			continue
		}
		seen[n] = true
		if rec, ok := s.st.Get(n); ok {
			recs = append(recs, rec)
		}
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Received.Before(recs[j].Received) })
	return recs
}

func readReportReq(w http.ResponseWriter, r *http.Request) (reportReq, bool) {
	var q reportReq
	if err := readBody(r, &q); err != nil {
		fail(w, 400, err)
		return q, false
	}
	if len(q.IDs) > 5000 {
		fail(w, 400, errors.New("too many records in the report"))
		return q, false
	}
	return q, true
}

// reportList answers POST /api/report/list {ids}: the rows of the table in the Report tab.
func (s *Server) reportList(w http.ResponseWriter, r *http.Request, id Identity) {
	q, ok := readReportReq(w, r)
	if !ok {
		return
	}
	dist := s.withDist(id)
	out := []recordMsg{}
	for _, rec := range s.reportRecords(q.IDs) {
		out = append(out, brief(rec, dist))
	}
	writeJSON(w, 200, map[string]any{"records": out})
}

// reportText answers POST /api/report/text {ids, info}: the report as plain text, in time order. info (the operator's
// personal information, administrators only) goes before or after the messages, or is left out.
func (s *Server) reportText(w http.ResponseWriter, r *http.Request, id Identity) {
	q, ok := readReportReq(w, r)
	if !ok {
		return
	}
	recs := s.reportRecords(q.IDs)
	info := ""
	if mode := q.Info; id.Role == Admin && (mode == "header" || mode == "footer") {
		info = strings.TrimSpace(s.run.Config().Operator.Info)
	}
	mode := q.Info
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
