package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"navtex/internal/store"
)

// The report: records a user has collected for a reception report. The list of record IDs lives in the user's own
// browser (every user, administrator or viewer, has their own); the server only turns a list of IDs into rows or text,
// applying the same privacy rules as everywhere else (a viewer gets no distances and no personal information).

type reportReq struct {
	IDs      []int64 `json:"ids"`
	Info     string  `json:"info"`
	Comments *bool   `json:"comments"` // false: the comments of the records are left out (absent: included)
}

// reportOrder is the order of the frequency groups in a report: the NAVTEX channels as listed here, then any other
// frequency (ascending).
var reportOrder = []int{518000, 490000, 486000, 424000, 4209500}

func reportRank(hz int) int {
	for i, f := range reportOrder {
		if f == hz {
			return i
		}
	}
	return len(reportOrder)
}

// The columns of the summary table (a monospaced font lines them up). The station and country have 33 characters (the longest
// in the station table), the distance is right-aligned in 9. The KHz and CALL columns are as wide as the longest value in the
// report, at least 3: a report of 518, 490, 486 or 424 kHz stations with three-letter callsigns is exactly 69 characters wide,
// and one with 4209.5 kHz or a callsign like DDH49 or CTV-251 is wider, the same in every frequency group.
const (
	reportStationW = 33
	reportDistW    = 9
	reportMaxCall  = 12
)

// reportRow is one line of the summary table, before it is laid out.
type reportRow struct {
	when                         time.Time
	khz, id, call, station, dist string
	comment                      string
}

func (r reportRow) widths() (int, int) { return len(r.khz), len([]rune(r.call)) }

// reportWidths gives the widths of the KHz and CALL columns for a report.
func reportWidths(rows []reportRow) (khz, call int) {
	khz, call = 3, 3
	for _, r := range rows {
		k, c := r.widths()
		if k > khz {
			khz = k
		}
		if c > call {
			call = c
		}
	}
	if call > reportMaxCall {
		call = reportMaxCall
	}
	return khz, call
}

// reportHeader is the column heading for those widths.
func reportHeader(khz, call int) string {
	return fmt.Sprintf("YYYYMMDD HHMM %-*s ID  %-*s %-*s%*s", khz, "KHz", call+1, "CALL", reportStationW, "STATION,COUNTRY", reportDistW, "DIST (Km)")
}

// text lays the row out: the columns of the heading, then the comment (if any) after them.
func (r reportRow) text(khz, call int) string {
	c := []rune(r.call)
	if len(c) > call {
		c = c[:call]
	}
	line := fmt.Sprintf("%s %-*s %s %-*s %-*s%*s", r.when.UTC().Format("20060102 1504"), khz, r.khz, r.id, call, string(c), reportStationW, fitStation(r.station, reportStationW), reportDistW, r.dist)
	if r.comment != "" {
		return line + " [" + r.comment + "]"
	}
	return strings.TrimRight(line, " ")
}

// fitStation shortens "Name,CCC" to w characters (a station added by the user may have a longer name): the name is cut,
// the country stays.
func fitStation(name string, w int) string {
	r := []rune(name)
	if len(r) <= w {
		return name
	}
	cc := ""
	if i := strings.LastIndex(name, ","); i >= 0 {
		cc = name[i:]
	}
	keep := w - len([]rune(cc))
	if keep < 1 {
		return string(r[:w])
	}
	return strings.TrimRight(string([]rune(name[:len(name)-len(cc)])[:keep]), " ") + cc
}

// reportRecords returns the stored records for ids, once each; IDs that no longer exist are dropped. They are grouped by
// frequency (518, 490, 486, 424, 4209.5 kHz, any other after those) and in time order within a group.
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
	sort.SliceStable(recs, func(i, j int) bool {
		ri, rj := reportRank(recs[i].Channel), reportRank(recs[j].Channel)
		if ri != rj {
			return ri < rj
		}
		if ri == len(reportOrder) && recs[i].Channel != recs[j].Channel {
			return recs[i].Channel < recs[j].Channel
		}
		return recs[i].Received.Before(recs[j].Received)
	})
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

// reportText answers POST /api/report/text {ids, info}: the report as plain text, grouped by frequency and in time order. info (the operator's
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
	rows := make([]reportRow, len(recs))
	for i, rec := range recs {
		call, name := "???", "Unknown,??"
		if st, ok := s.cat.Station(rec.StationID); ok {
			call = st.Callsign
			name = st.Name + "," + st.CountryCode
		}
		b1 := rec.B1
		if b1 == "" {
			b1 = "?"
		}
		km := ""
		if dist && rec.DistanceKm > 0 {
			km = fmt.Sprintf("%.0f", rec.DistanceKm)
		}
		comment := rec.Comment
		if q.Comments != nil && !*q.Comments {
			comment = ""
		}
		rows[i] = reportRow{when: rec.Received, khz: store.FormatKHz(rec.Channel), id: fmt.Sprintf("$%02d%s", rec.NavArea, b1), call: call, station: name, dist: km, comment: comment}
	}
	khzW, callW := reportWidths(rows)
	head := reportHeader(khzW, callW)
	// the summary table: the column heading starts every frequency group (a frequency without messages has no group)
	for i, rec := range recs {
		if i == 0 || rec.Channel != recs[i-1].Channel {
			if i > 0 {
				put("")
			}
			put("%s", head)
			put("%s", strings.Repeat("-", len(head)))
		}
		put("%s", rows[i].text(khzW, callW))
	}
	if len(recs) > 0 {
		put("")
		put("RAW LOG")
	}
	for n, rec := range recs {
		put("---")
		put("%s", rows[n].text(khzW, callW))
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
