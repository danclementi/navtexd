package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"navtex/internal/autocorr"
	"navtex/internal/sitor"
	"navtex/internal/store"
)

func (s *Server) recordRoutes(mux *http.ServeMux, tunnel bool) {
	mux.HandleFunc("GET /api/messages", s.viewer(tunnel, s.messages))
	mux.HandleFunc("GET /api/messages/{id}", s.viewer(tunnel, s.message))
	mux.HandleFunc("PATCH /api/messages/{id}", s.admin(tunnel, s.patchMessage))
	mux.HandleFunc("DELETE /api/messages/{id}", s.admin(tunnel, s.deleteMessage))
	mux.HandleFunc("POST /api/messages/delete", s.admin(tunnel, s.deleteMessages))
	mux.HandleFunc("GET /api/messages/undo", s.admin(tunnel, s.undoInfo))
	mux.HandleFunc("POST /api/messages/undo", s.admin(tunnel, s.undoDelete))
	mux.HandleFunc("POST /api/maintenance/delete", s.admin(tunnel, s.maintDelete))
	mux.HandleFunc("POST /api/maintenance/compact", s.admin(tunnel, s.maintCompact))
	mux.HandleFunc("POST /api/maintenance/reidentify", s.admin(tunnel, s.maintReidentify))
	mux.HandleFunc("POST /api/maintenance/recorrect", s.admin(tunnel, s.maintRecorrect))
	mux.HandleFunc("GET /api/stations", s.viewer(tunnel, s.stationList))
	mux.HandleFunc("GET /api/stations/export", s.viewer(tunnel, s.stationExport))
	mux.HandleFunc("PUT /api/stations/{id}/keywords", s.admin(tunnel, s.stationKeywords))
	mux.HandleFunc("POST /api/stations/import", s.admin(tunnel, s.stationImport))
	mux.HandleFunc("POST /api/report/list", s.viewer(tunnel, s.reportList))
	mux.HandleFunc("POST /api/report/text", s.viewer(tunnel, s.reportText))
	mux.HandleFunc("POST /api/reverse", s.reverse)
	s.clipRoutes(mux, tunnel)
}

// withDist says whether distances and bearings (which reveal where the receiver is) may be shown to this client.
func (s *Server) withDist(id Identity) bool {
	return id.Role == Admin || s.run.Config().Operator.Public
}

func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad date %q (want yyyy-mm-dd)", v)
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, id Identity) {
	q := r.URL.Query()
	f := store.Filter{Text: q.Get("text"), B1: q.Get("b1"), Kind: q.Get("kind"), Newest: q.Get("order") == "newest", Unidentified: q.Get("unid") == "1"}
	f.Channel, _ = strconv.Atoi(q.Get("freq"))
	f.Rcv, _ = strconv.Atoi(q.Get("rcv"))
	if v := q.Get("navarea"); v != "" { // a list: 1,2,5
		f.NavAreas = map[int]bool{}
		for _, t := range strings.Split(v, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
				f.NavAreas[n] = true
			}
		}
	}
	if v := q.Get("b1"); v != "" && v != "*" {
		f.B1 = ""
		f.B1s = map[string]bool{}
		for _, t := range strings.Split(v, ",") {
			if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
				f.B1s[t] = true
			}
		}
	}
	f.OmitUnid = q.Get("omitunid") == "1"
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	var err error
	if f.From, err = parseTime(q.Get("from")); err != nil {
		fail(w, 400, err)
		return
	}
	if f.To, err = parseTime(q.Get("to")); err != nil {
		fail(w, 400, err)
		return
	}
	if !f.To.IsZero() && len(q.Get("to")) == 10 {
		f.To = f.To.Add(24 * time.Hour) // a date means the whole day
	}
	dist := s.withDist(id)
	if dist {
		f.MaxDistance, _ = strconv.ParseFloat(q.Get("maxkm"), 64)
		f.MinDistance, _ = strconv.ParseFloat(q.Get("minkm"), 64)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	recs, total := s.st.Query(f)
	out := make([]recordMsg, len(recs))
	for i, x := range recs {
		out[i] = brief(x, dist)
	}
	writeJSON(w, 200, map[string]any{"total": total, "all": s.st.Count(), "records": out})
}

func (s *Server) message(w http.ResponseWriter, r *http.Request, id Identity) {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rec, ok := s.st.Get(n)
	if !ok {
		fail(w, 404, errors.New("no such record"))
		return
	}
	if !s.withDist(id) {
		rec.DistanceKm, rec.BearingDeg = 0, 0
	}
	// stretches that look as if they were decoded in the wrong shift, per line (empty when there are none)
	c, cfg := s.run.Corrector()
	det := struct {
		store.Record
		Rev [][]autocorr.Reversal `json:"rv,omitempty"`
		Eff []string              `json:"eff,omitempty"` // the lines with the operator's revisions applied
	}{Record: rec}
	if len(rec.Revs) > 0 {
		det.Eff = rec.EffectiveLines()
	}
	found := false
	det.Rev = make([][]autocorr.Reversal, len(rec.Lines))
	for i, l := range rec.Lines {
		if det.Rev[i] = c.Reversals(l.Text, cfg); len(det.Rev[i]) > 0 {
			found = true
		}
	}
	if !found {
		det.Rev = nil
	}
	writeJSON(w, 200, det)
}

func (s *Server) publishRecord(action string, r store.Record) {
	m := brief(r, false)
	m.Action = action
	s.hub.Publish("record", m, false, false)
}

// patchMessage edits B1, NAVAREA and the comment of a record. Editing B1 or NAVAREA marks the record as set by the
// operator: the station is then looked up for that pair and later re-identification leaves the pair alone.
func (s *Server) patchMessage(w http.ResponseWriter, r *http.Request, id Identity) {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var body struct {
		Version int     `json:"v"`
		B1      *string `json:"b1"`
		NavArea *int    `json:"area"`
		Comment *string `json:"cmt"`
		Rev     *revOp  `json:"rev"`
		Station *int    `json:"station"` // choose this station (ID); sets B1 and NAVAREA to its own; 0 = no choice
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	rec, ok := s.st.Get(n)
	if !ok {
		fail(w, 404, errors.New("no such record"))
		return
	}
	if rec.Version != body.Version {
		fail(w, 409, errors.New("the record was changed elsewhere; reloaded"))
		return
	}
	if body.B1 != nil {
		b := strings.ToUpper(strings.TrimSpace(*body.B1))
		if b != "" && b != "?" && !(len(b) == 1 && b[0] >= 'A' && b[0] <= 'Z') {
			fail(w, 400, errors.New("B1 must be a capital letter (or empty / ?)"))
			return
		}
		rec.B1, rec.Manual, rec.Picked = b, true, 0
		if body.NavArea == nil {
			rec.NavArea = 0
		}
	}
	if body.NavArea != nil {
		if *body.NavArea < 0 || *body.NavArea > 99 {
			fail(w, 400, errors.New("NAVAREA must be 0 to 99"))
			return
		}
		rec.NavArea, rec.Manual, rec.Picked = *body.NavArea, true, 0
	}
	if body.Station != nil {
		if *body.Station == 0 {
			rec.Picked = 0
		} else {
			st, ok := s.cat.Station(*body.Station)
			if !ok || st.FreqKHz != rec.Channel {
				fail(w, 400, errors.New("that station does not transmit on this channel"))
				return
			}
			rec.B1, rec.NavArea, rec.Manual, rec.Picked = st.ID, st.NavArea, true, *body.Station
		}
	}
	if body.Comment != nil {
		rec.Comment = strings.TrimSpace(*body.Comment)
	}
	if body.B1 != nil || body.NavArea != nil || body.Station != nil {
		s.cat.Enrich(&rec)
	}
	if body.Rev != nil {
		note, err := applyRevision(&rec, body.Rev)
		if err != nil {
			fail(w, 400, err)
			return
		}
		s.run.Correct(&rec) // the autocorrection works on the revised text
		s.run.Log("info", fmt.Sprintf("record %s revised: %s", hexID(rec.ID), note))
	}
	saved, err := s.st.Update(rec)
	switch {
	case errors.Is(err, store.ErrConflict):
		fail(w, 409, errors.New("the record was changed elsewhere; reloaded"))
		return
	case err != nil:
		fail(w, 500, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("record %s edited: B1 %q, NAVAREA %d, station %s", hexID(saved.ID), saved.B1, saved.NavArea, saved.Station))
	s.publishRecord("update", saved)
	writeJSON(w, 200, saved)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request, id Identity) {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.st.Delete(n)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if c == 0 {
		fail(w, 404, errors.New("no such record"))
		return
	}
	s.run.Log("info", fmt.Sprintf("record %s deleted (Undo brings it back)", hexID(n)))
	s.hub.Publish("record", recordMsg{Action: "delete", ID: n}, false, false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// hexID is how record numbers are shown: 0x0000028E.
func hexID(id int64) string { return fmt.Sprintf("0x%08X", id) }

// deleteMessages deletes several records as one step that Undo reverses.
func (s *Server) deleteMessages(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := readBody(r, &body); err != nil || len(body.IDs) == 0 {
		fail(w, 400, errors.New("no records given"))
		return
	}
	c, err := s.st.Delete(body.IDs...)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if c == 0 {
		fail(w, 404, errors.New("no such records"))
		return
	}
	if c == 1 {
		s.run.Log("info", fmt.Sprintf("record %s deleted (Undo brings it back)", hexID(body.IDs[0])))
	} else {
		s.run.Log("info", fmt.Sprintf("%d records deleted (Undo brings them back)", c))
	}
	for _, n := range body.IDs {
		s.hub.Publish("record", recordMsg{Action: "delete", ID: n}, false, false)
	}
	writeJSON(w, 200, map[string]any{"deleted": c, "undo": s.undoState()})
}

func (s *Server) undoState() map[string]any {
	b, ok := s.st.LastDeletion()
	if !ok {
		return map[string]any{"available": false}
	}
	return map[string]any{"available": true, "count": b.Count, "at": b.At.UnixMilli()}
}

func (s *Server) undoInfo(w http.ResponseWriter, r *http.Request, id Identity) {
	writeJSON(w, 200, s.undoState())
}

// undoDelete brings back the records of the last deletion.
func (s *Server) undoDelete(w http.ResponseWriter, r *http.Request, id Identity) {
	recs, err := s.st.Undo()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, 404, errors.New("nothing to undo"))
		} else {
			fail(w, 500, err)
		}
		return
	}
	if len(recs) == 1 {
		s.run.Log("info", fmt.Sprintf("record %s restored", hexID(recs[0].ID)))
	} else {
		s.run.Log("info", fmt.Sprintf("%d records restored", len(recs)))
	}
	s.hub.Publish("record", recordMsg{Action: "restore"}, false, false)
	writeJSON(w, 200, map[string]any{"restored": len(recs), "undo": s.undoState()})
}

func (s *Server) maintDelete(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		OlderThan    string  `json:"older_than"`
		CloserThanKm float64 `json:"closer_than_km"`
		UnidOnly     bool    `json:"unid_only"`
		DryRun       bool    `json:"dry_run"`
		Compact      bool    `json:"compact"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	older, err := parseTime(body.OlderThan)
	if err != nil {
		fail(w, 400, err)
		return
	}
	f := store.Filter{To: older}
	if body.OlderThan != "" && len(body.OlderThan) == 10 {
		f.To = older // records before the start of that day
	}
	switch {
	case body.UnidOnly:
		f.Unidentified = true
	case body.CloserThanKm > 0:
		f.MaxDistance = body.CloserThanKm
	}
	if body.OlderThan == "" && !body.UnidOnly && body.CloserThanKm <= 0 {
		fail(w, 400, errors.New("choose what to delete: a date, a distance or unidentified records"))
		return
	}
	if body.DryRun {
		_, total := s.st.Query(store.Filter{To: f.To, Unidentified: f.Unidentified, MaxDistance: f.MaxDistance, Limit: 1})
		writeJSON(w, 200, map[string]any{"count": total})
		return
	}
	n, err := s.st.DeleteWhere(f)
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("maintenance: %d records deleted", n))
	if body.Compact {
		if err := s.st.Compact(); err != nil {
			fail(w, 500, err)
			return
		}
	}
	s.hub.Publish("record", recordMsg{Action: "delete"}, false, false)
	writeJSON(w, 200, map[string]any{"deleted": n, "remaining": s.st.Count()})
}

func (s *Server) maintCompact(w http.ResponseWriter, r *http.Request, id Identity) {
	if err := s.st.Compact(); err != nil {
		fail(w, 500, err)
		return
	}
	s.run.Log("info", "message file compacted")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) maintReidentify(w http.ResponseWriter, r *http.Request, id Identity) {
	n, err := s.cat.Reidentify(s.st)
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("stations re-identified: %d records changed", n))
	s.hub.Publish("record", recordMsg{Action: "update"}, false, false)
	writeJSON(w, 200, map[string]any{"changed": n})
}

// maintRecorrect redoes the word autocorrection of every stored record (after the vocabulary or the options changed).
func (s *Server) maintRecorrect(w http.ResponseWriter, r *http.Request, id Identity) {
	recs, _ := s.st.Query(store.Filter{})
	changed := 0
	for _, rec := range recs {
		old := rec.Corrected
		s.run.Correct(&rec)
		if rec.Corrected == old {
			continue
		}
		if _, err := s.st.Update(rec); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			fail(w, 500, err)
			return
		}
		changed++
	}
	s.run.Log("info", fmt.Sprintf("autocorrection redone: %d records changed", changed))
	s.hub.Publish("record", recordMsg{Action: "update"}, false, false)
	writeJSON(w, 200, map[string]any{"changed": changed})
}

// ---- stations ----

type stationMsg struct {
	ID       int       `json:"id"`
	NavArea  int       `json:"area"`
	Freq     int       `json:"freq"`
	B1       string    `json:"b1"`
	Callsign string    `json:"call"`
	Name     string    `json:"name"`
	CC       string    `json:"cc"`
	Country  string    `json:"country"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	Times    [6]string `json:"times"`
	GID      []string  `json:"gid"`
	UID      []string  `json:"uid"`
	Km       float64   `json:"km,omitempty"`  // from the receiver; only for clients allowed to know it
	Brg      float64   `json:"brg,omitempty"` // bearing from the receiver
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) stationList(w http.ResponseWriter, r *http.Request, id Identity) {
	ver, list := s.cat.Stations()
	out := make([]stationMsg, len(list))
	op := s.run.Config().Operator
	var lat, lon *float64
	if s.withDist(id) {
		lat, lon = &op.LatDeg, &op.LonDeg
	}
	for i, x := range list {
		out[i] = stationMsgFor(i+1, x, lat, lon)
	}
	writeJSON(w, 200, map[string]any{"version": ver, "max_keyword_chars": 300, "stations": out})
}

func (s *Server) stationExport(w http.ResponseWriter, r *http.Request, id Identity) {
	w.Header().Set("Content-Type", "text/plain; charset=us-ascii")
	w.Header().Set("Content-Disposition", `attachment; filename="YAND_STATIONS.TXT"`)
	w.Write(s.cat.Export())
}

func (s *Server) stationKeywords(w http.ResponseWriter, r *http.Request, id Identity) {
	n, _ := strconv.Atoi(r.PathValue("id"))
	var body struct {
		Type     string   `json:"type"`
		Keywords []string `json:"keywords"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.cat.SetKeywords(n, body.Type, body.Keywords); err != nil {
		fail(w, 400, err)
		return
	}
	if st, ok := s.cat.Station(n); ok {
		s.run.Log("info", fmt.Sprintf("%s keywords of %s %s changed", strings.ToUpper(body.Type), st.Callsign, st.Name))
	}
	s.hub.Publish("stations", map[string]any{"changed": n}, false, false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) stationImport(w http.ResponseWriter, r *http.Request, id Identity) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		fail(w, 400, err)
		return
	}
	n, err := s.cat.Import(data)
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("station table imported: %d stations", n))
	out := map[string]any{"stations": n}
	if r.URL.Query().Get("reidentify") == "1" {
		c, err := s.cat.Reidentify(s.st)
		if err != nil {
			fail(w, 500, err)
			return
		}
		out["changed"] = c
		s.hub.Publish("record", recordMsg{Action: "update"}, false, false)
	}
	s.hub.Publish("stations", map[string]any{"changed": 0}, false, false)
	writeJSON(w, 200, out)
}

// reverse swaps letters and figures in a piece of text (the shift reverser). It changes nothing on the server.
func (s *Server) reverse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"text": sitor.Reverse(body.Text)})
}

// revOp is an edit of the operator's revisions of a record: add (replace a stretch of a raw line), del (remove one) or
// clear (remove all). A stretch is given by Start/End (byte offsets in the raw line) or by Find (the n-th occurrence,
// Occ counted from 0, of that text in the raw line).
type revOp struct {
	Op    string `json:"op"`
	Line  int    `json:"l"`
	Start int    `json:"s"`
	End   int    `json:"e"`
	Find  string `json:"find"`
	Occ   int    `json:"occ"`
	Text  string `json:"t"`
	Index int    `json:"i"`
}

func applyRevision(rec *store.Record, o *revOp) (string, error) {
	switch o.Op {
	case "clear":
		rec.Revs = nil
		return "all revisions removed", nil
	case "del":
		if o.Index < 0 || o.Index >= len(rec.Revs) {
			return "", errors.New("no such revision")
		}
		v := rec.Revs[o.Index]
		rec.Revs = append(append([]store.Revision{}, rec.Revs[:o.Index]...), rec.Revs[o.Index+1:]...)
		return fmt.Sprintf("line %d: %q put back", v.Line+1, v.Old), nil
	case "add":
		if o.Line < 0 || o.Line >= len(rec.Lines) {
			return "", errors.New("no such line")
		}
		raw := rec.Lines[o.Line].Text
		start, end := o.Start, o.End
		if o.Find != "" {
			at := -1
			for n, from := 0, 0; n <= o.Occ; n++ {
				i := strings.Index(raw[from:], o.Find)
				if i < 0 {
					at = -1
					break
				}
				at = from + i
				from = at + len(o.Find)
			}
			if at < 0 {
				return "", errors.New("that text is not in the received line as it is (it may contain autocorrected words or an earlier revision): select a part that was received unchanged")
			}
			start, end = at, at+len(o.Find)
		}
		if start < 0 || end > len(raw) || start >= end {
			return "", errors.New("bad range")
		}
		t := o.Text
		if t == "" || len(t) > 400 {
			return "", errors.New("the replacement must be 1 to 400 characters")
		}
		for _, c := range []byte(t) {
			if c < 32 || c > 126 || c == '<' || c == '>' {
				return "", errors.New("the replacement may only contain printable ASCII characters, without < and >")
			}
		}
		for _, v := range rec.Revs {
			if v.Line == o.Line && start < v.End && v.Start < end {
				return "", errors.New("that stretch overlaps an earlier revision; put the earlier one back first")
			}
		}
		rec.Revs = append(rec.Revs, store.Revision{Line: o.Line, Start: start, End: end, Old: raw[start:end], New: t})
		return fmt.Sprintf("line %d: %q replaced by %q", o.Line+1, raw[start:end], t), nil
	}
	return "", errors.New("unknown revision operation")
}
