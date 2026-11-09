package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"navtex/internal/autocorr"
	"navtex/internal/catalog"
	"navtex/internal/sitor"
	"navtex/internal/store"
	"navtex/internal/yand"
)

func (s *Server) recordRoutes(mux *http.ServeMux, tunnel bool) {
	mux.HandleFunc("GET /api/messages", s.viewer(tunnel, s.messages))
	mux.HandleFunc("GET /api/messages/locate", s.viewer(tunnel, s.locate))
	mux.HandleFunc("GET /api/messages/{id}", s.viewer(tunnel, s.message))
	mux.HandleFunc("PATCH /api/messages/{id}", s.admin(tunnel, s.patchMessage))
	mux.HandleFunc("DELETE /api/messages/{id}", s.admin(tunnel, s.deleteMessage))
	mux.HandleFunc("POST /api/messages/delete", s.admin(tunnel, s.deleteMessages))
	mux.HandleFunc("GET /api/messages/undo", s.admin(tunnel, s.undoInfo))
	mux.HandleFunc("POST /api/messages/undo", s.admin(tunnel, s.undoDelete))
	mux.HandleFunc("GET /api/trash", s.admin(tunnel, s.trashList))
	mux.HandleFunc("POST /api/trash/restore", s.admin(tunnel, s.trashRestore))
	mux.HandleFunc("POST /api/maintenance/compact", s.admin(tunnel, s.maintCompact))
	mux.HandleFunc("POST /api/maintenance/prune", s.admin(tunnel, s.maintPrune))
	mux.HandleFunc("GET /api/database", s.admin(tunnel, s.dbInfo))
	mux.HandleFunc("GET /api/database/export", s.admin(tunnel, s.dbExport))
	mux.HandleFunc("POST /api/database/import", s.admin(tunnel, s.dbImport))
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

// logFilter reads the log filter from the query of a request (the Log tab's filter dialog); withDist says whether the distance
// limits are honoured (viewers may not be told distances).
func logFilter(q url.Values, withDist bool) (store.Filter, error) {
	f := store.Filter{Text: q.Get("text"), B1: q.Get("b1"), Kind: q.Get("kind"), Newest: q.Get("order") == "newest", Unidentified: q.Get("unid") == "1"}
	if k, err := strconv.ParseFloat(q.Get("freq"), 64); err == nil && k > 0 { // kHz (4209.5)
		f.Channel = int(math.Round(k * 1000))
	}
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
		return f, err
	}
	if f.To, err = parseTime(q.Get("to")); err != nil {
		return f, err
	}
	if !f.To.IsZero() {
		switch len(q.Get("to")) {
		case 10:
			f.To = f.To.Add(24 * time.Hour) // a date means the whole day
		case 16:
			f.To = f.To.Add(time.Minute) // yyyy-mm-ddThh:mm includes that minute
		}
	}
	if withDist {
		f.MaxDistance, _ = strconv.ParseFloat(q.Get("maxkm"), 64)
		f.MinDistance, _ = strconv.ParseFloat(q.Get("minkm"), 64)
	}
	return f, nil
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, id Identity) {
	dist := s.withDist(id)
	f, err := logFilter(r.URL.Query(), dist)
	if err != nil {
		fail(w, 400, err)
		return
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

// locate gives the position of record ?id= in the list the same filter and order parameters select (-1: not in it).
func (s *Server) locate(w http.ResponseWriter, r *http.Request, id Identity) {
	f, err := logFilter(r.URL.Query(), s.withDist(id))
	if err != nil {
		fail(w, 400, err)
		return
	}
	n, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	writeJSON(w, 200, map[string]any{"index": s.st.Locate(f, n)})
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
		Ch  float64               `json:"ch"` // kHz (the record's own value is in Hz)
		Rev [][]autocorr.Reversal `json:"rv,omitempty"`
		Eff []string              `json:"eff,omitempty"` // the lines with the operator's revisions applied
		Why *catalog.Why          `json:"why,omitempty"` // how the station was identified
	}{Record: rec, Ch: store.KHz(rec.Channel)}
	det.Why = s.cat.Explain(rec, s.withDist(id))
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
		Revs    []revOp `json:"revs"`    // several revisions in one step (a selection over several lines), all or none
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
			if !ok || st.FreqKHz*1000 != rec.Channel {
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
	ops := body.Revs
	if body.Rev != nil {
		ops = append([]revOp{*body.Rev}, ops...)
	}
	if len(ops) > 200 {
		fail(w, 400, errors.New("too many revisions at once"))
		return
	}
	if len(ops) > 0 {
		var notes []string
		for i := range ops {
			note, err := applyRevision(&rec, &ops[i])
			if err != nil {
				fail(w, 400, err)
				return
			}
			notes = append(notes, note)
		}
		s.run.Correct(&rec) // the autocorrection works on the revised text
		s.run.Log("info", fmt.Sprintf("record %s revised: %s", hexID(rec.ID), strings.Join(notes, "; ")))
	}
	var parts []string // what the operator did, for the Undo button
	if body.B1 != nil {
		parts = append(parts, "B1 change")
	}
	if body.NavArea != nil {
		parts = append(parts, "NAVAREA change")
	}
	if body.Station != nil {
		parts = append(parts, "station choice")
	}
	if body.Comment != nil {
		parts = append(parts, "comment change")
	}
	if len(ops) > 0 {
		parts = append(parts, "text revision")
	}
	if len(parts) == 0 {
		parts = []string{"edit"}
	}
	saved, err := s.st.UpdateUndoable(rec, strings.Join(parts, " and "))
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
	return map[string]any{"available": true, "count": b.Count, "at": b.At.UnixMilli(), "kind": b.Kind, "desc": b.Desc, "id": b.RecID, "hex": hexID(b.RecID), "rx": b.Rx.UnixMilli()}
}

func (s *Server) undoInfo(w http.ResponseWriter, r *http.Request, id Identity) {
	writeJSON(w, 200, s.undoState())
}

// undoDelete brings back the records of the last deletion.
func (s *Server) undoDelete(w http.ResponseWriter, r *http.Request, id Identity) {
	last, _ := s.st.LastDeletion()
	recs, err := s.st.Undo()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, 404, errors.New("nothing to undo"))
		} else {
			fail(w, 500, err)
		}
		return
	}
	switch {
	case last.Kind == "edit" && len(recs) == 1:
		s.run.Log("info", fmt.Sprintf("record %s: %s undone", hexID(recs[0].ID), last.Desc))
		s.publishRecord("update", recs[0])
	case len(recs) == 1:
		s.run.Log("info", fmt.Sprintf("record %s restored", hexID(recs[0].ID)))
	default:
		s.run.Log("info", fmt.Sprintf("%d records restored", len(recs)))
	}
	s.hub.Publish("record", recordMsg{Action: "restore"}, false, false)
	ids := make([]int64, len(recs))
	for i, x := range recs {
		ids[i] = x.ID
	}
	writeJSON(w, 200, map[string]any{"restored": len(recs), "kind": last.Kind, "ids": ids, "undo": s.undoState()})
}

// trashList lists the deleted messages (the newest deletion first) for the Database dialog; text= searches them, limit and
// offset page through them.
func (s *Server) trashList(w http.ResponseWriter, r *http.Request, id Identity) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	recs, total := s.st.Trash(store.Filter{Text: strings.TrimSpace(q.Get("text")), Limit: limit, Offset: max(offset, 0)})
	type item struct {
		ID      int64   `json:"id"`
		Hex     string  `json:"hex"`
		Rx      int64   `json:"rx"`
		Deleted int64   `json:"deleted"`
		Ch      float64 `json:"ch"` // kHz
		Kind    string  `json:"kind"`
		Station string  `json:"st"`
		Text    string  `json:"text"`
	}
	out := make([]item, len(recs))
	for i, r := range recs {
		t := strings.Join(strings.Fields(r.Text()), " ")
		if rs := []rune(t); len(rs) > 120 {
			t = string(rs[:120]) + "\u2026"
		}
		out[i] = item{r.ID, hexID(r.ID), r.Received.UnixMilli(), r.DeletedAt.UnixMilli(), store.KHz(r.Channel), r.Kind, r.Station, t}
	}
	writeJSON(w, 200, map[string]any{"total": total, "records": out})
}

// trashRestore brings back chosen messages from the trash, whichever deletion they belong to.
func (s *Server) trashRestore(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := readBody(r, &body); err != nil || len(body.IDs) == 0 {
		fail(w, 400, errors.New("no records given"))
		return
	}
	recs, err := s.st.Restore(body.IDs...)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if len(recs) == 0 {
		fail(w, 404, errors.New("those messages are no longer in the trash"))
		return
	}
	if len(recs) == 1 {
		s.run.Log("info", fmt.Sprintf("record %s restored from the trash", hexID(recs[0].ID)))
	} else {
		s.run.Log("info", fmt.Sprintf("%d records restored from the trash", len(recs)))
	}
	s.hub.Publish("record", recordMsg{Action: "restore"}, false, false)
	ids := make([]int64, len(recs))
	for i, x := range recs {
		ids[i] = x.ID
	}
	writeJSON(w, 200, map[string]any{"restored": len(recs), "ids": ids, "undo": s.undoState()})
}

// dbInfo describes the log database for the Database dialog.
func (s *Server) dbInfo(w http.ResponseWriter, r *http.Request, id Identity) {
	writeJSON(w, 200, map[string]any{"records": s.st.Count(), "bytes": s.st.Size(), "backups": s.st.Backups(), "undo": s.undoState()})
}

// maintCompact empties the old trash and shrinks the database file (after saving a copy; the last three copies are kept).
func (s *Server) maintCompact(w http.ResponseWriter, r *http.Request, id Identity) {
	before := s.st.Size()
	name, err := s.st.Backup("before-compact", 3)
	if err != nil {
		fail(w, 500, fmt.Errorf("could not save a backup first: %w", err))
		return
	}
	if err := s.st.Compact(); err != nil {
		fail(w, 500, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("message database compacted (%d -> %d bytes), backup %s", before, s.st.Size(), name))
	writeJSON(w, 200, map[string]any{"ok": true, "before": before, "after": s.st.Size(), "backup": name})
}

// pruneFilter turns a request of the Database dialog's "Prune" section into a filter. The modes follow YaND's log maintenance:
// "date" (received before a UTC date), "distance" (closer than km), "both" (both at once) and "unid" (records without a station).
// The first three keep records without a station: their distance is not known. freq (kHz, 0 = all) limits any of them to a channel.
func pruneFilter(mode, before string, km, freq float64) (store.Filter, error) {
	var f store.Filter
	if freq < 0 || math.IsNaN(freq) || math.IsInf(freq, 0) {
		return f, errors.New("bad frequency")
	}
	if freq > 0 {
		f.Channel = int(math.Round(freq * 1000))
	}
	if mode == "date" || mode == "both" {
		if len(before) != 10 {
			return f, errors.New("give the date (yyyy-mm-dd)")
		}
		t, err := parseTime(before)
		if err != nil {
			return f, err
		}
		f.To = t // received before 00:00 UTC of that day
	}
	if mode == "distance" || mode == "both" {
		if !(km > 0) || km > 40100 || math.IsInf(km, 0) {
			return f, errors.New("give a distance in km, more than 0")
		}
		f.MaxDistance = km
	}
	switch mode {
	case "date":
		f.OmitUnid = true
	case "distance", "both":
	case "unid":
		f.Unidentified = true
	default:
		return f, errors.New("no such way to prune the log")
	}
	return f, nil
}

// maintPrune counts (dry) or deletes the records matching a pruning rule. The deletion is one action that Undo takes back (the
// records stay in the trash for 30 days, which compacting does not shorten, unless "erase" asks for a deletion for good); a copy of the database is saved first.
func (s *Server) maintPrune(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Mode    string  `json:"mode"`
		Before  string  `json:"before"`
		Km      float64 `json:"km"`
		Freq    float64 `json:"freq"`
		Dry     bool    `json:"dry"`
		Compact bool    `json:"compact"`
		Erase   bool    `json:"erase"` // delete for good: no trash, no Undo
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	f, err := pruneFilter(body.Mode, body.Before, body.Km, body.Freq)
	if err != nil {
		fail(w, 400, err)
		return
	}
	f.Limit = 1 // only the count is wanted
	_, n := s.st.Query(f)
	if body.Dry {
		writeJSON(w, 200, map[string]any{"matches": n, "records": s.st.Count()})
		return
	}
	if n == 0 {
		writeJSON(w, 200, map[string]any{"deleted": 0, "records": s.st.Count(), "undo": s.undoState()})
		return
	}
	backup, err := s.st.Backup("before-prune", 3)
	if err != nil {
		fail(w, 500, fmt.Errorf("could not save a backup first: %w", err))
		return
	}
	f.Limit = 0
	del, how := s.st.DeleteWhere, "Undo brings them back"
	if body.Erase {
		del, how = s.st.EraseWhere, "for good"
	}
	c, err := del(f)
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("log pruned (%s): %d records deleted (%s), backup %s", body.Mode, c, how, backup))
	out := map[string]any{"deleted": c, "backup": backup}
	if body.Compact {
		before := s.st.Size()
		if err := s.st.Compact(); err != nil {
			fail(w, 500, err)
			return
		}
		out["size_before"], out["size_after"] = before, s.st.Size()
	}
	s.hub.Publish("record", recordMsg{Action: "restore"}, false, false) // (makes the Log tab load its list again)
	out["records"], out["undo"] = s.st.Count(), s.undoState()
	writeJSON(w, 200, out)
}

// dbExport downloads the log (all records, or those matching the Log tab's filter parameters).
func (s *Server) dbExport(w http.ResponseWriter, r *http.Request, id Identity) {
	f, err := logFilter(r.URL.Query(), true)
	if err != nil {
		fail(w, 400, err)
		return
	}
	name := "navtexd-log-" + time.Now().UTC().Format("20060102T150405Z") + ".jsonl"
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	n, err := s.st.Export(w, f)
	if err != nil {
		s.run.Log("warn", "log export: "+err.Error())
		return
	}
	s.run.Log("info", fmt.Sprintf("log exported: %d records", n))
}

// importReply is what an import says: the result, and for a file of YaND what it was.
type importReply struct {
	store.ImportResult
	Source string `json:"source,omitempty"`  // "YaND database" or "YaND report"
	NoText int    `json:"no_text,omitempty"` // records of a YaND database whose text is not in it (deleted or overwritten there)
}

// dbImport reads a log file from the request body; mode=replace or merge. The file is an export of this program, or the messages
// file of an earlier version, or a log of YaND (YAND_DB.ABS, or a reception report), recognized by its contents.
func (s *Server) dbImport(w http.ResponseWriter, r *http.Request, id Identity) {
	replace := r.URL.Query().Get("mode") == "replace"
	if r.URL.Query().Get("mode") != "replace" && r.URL.Query().Get("mode") != "merge" {
		fail(w, 400, errors.New("mode must be merge or replace"))
		return
	}
	body := bufio.NewReaderSize(http.MaxBytesReader(w, r.Body, 1<<30), 64<<10)
	head, _ := body.Peek(8192)
	rep := importReply{}
	var res store.ImportResult
	var err error
	switch yand.Kind(head) {
	case "abs":
		rep.Source = "YaND database"
		res, rep.NoText, err = s.importYaND(body, true, replace)
	case "report":
		rep.Source = "YaND report"
		res, _, err = s.importYaND(body, false, replace)
	default:
		res, err = s.st.Import(body, replace)
	}
	if err != nil {
		fail(w, 400, err)
		return
	}
	rep.ImportResult = res
	from := "log"
	if rep.Source != "" {
		from = rep.Source
	}
	if replace {
		s.run.Log("info", fmt.Sprintf("log replaced by an import of a %s: %d records (the old log of %d records is saved as backups/%s)", from, res.Added, res.Replaced, res.Backup))
	} else {
		s.run.Log("info", fmt.Sprintf("%s import (merge): %d records added, %d already there", from, res.Added, res.Skipped))
	}
	s.hub.Publish("record", recordMsg{Action: "restore"}, false, false)
	writeJSON(w, 200, rep)
}

// importYaND converts a YaND database (abs) or reception report from r and puts its records in the log. It returns how many
// records of a database have no text.
func (s *Server) importYaND(r io.Reader, abs, replace bool) (store.ImportResult, int, error) {
	var recs []store.Record
	noText := 0
	if abs {
		tmp, err := os.CreateTemp("", "yand-*.abs") // the database is read page by page: it is not held in memory
		if err != nil {
			return store.ImportResult{}, 0, err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		n, err := io.Copy(tmp, r)
		if err != nil {
			return store.ImportResult{}, 0, err
		}
		var st yand.Stats
		if recs, st, err = yand.ReadABS(tmp, n); err != nil {
			return store.ImportResult{}, 0, err
		}
		noText = st.NoText
	} else {
		var err error
		if recs, err = yand.ReadReport(r); err != nil {
			return store.ImportResult{}, 0, err
		}
	}
	place := func(*store.Record) {}
	if s.cat != nil {
		place = s.cat.Placer()
	}
	ptrs := make([]*store.Record, len(recs))
	for i := range recs {
		place(&recs[i])
		ptrs[i] = &recs[i]
	}
	res, err := s.st.ImportRecords(ptrs, replace)
	return res, noText, err
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
	n, backup, err := s.cat.Import(data)
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("station table replaced by an import: %d stations (the previous table is saved as backups/%s)", n, backup))
	out := map[string]any{"stations": n, "backup": backup}
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
		for _, c := range t {
			if (c < 32 || c > 126) && c != '£' || c == '<' || c == '>' { // (the pound sign is a NAVTEX character: the reverser makes it of an H)
				return "", errors.New("the replacement may only contain printable ASCII characters and the pound sign, without < and >")
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
