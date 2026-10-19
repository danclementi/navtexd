// Package store keeps the message log, the system log and the quality history in plain files inside one directory,
// using only the standard library (append-only JSON lines, replayed into memory on open). Everything is safe for
// concurrent use. The API is deliberately small so the implementation can be replaced by a database later.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// F2 is a float stored in JSON with exactly two decimals.
type F2 float64

// MarshalJSON writes the value in fixed format with two decimals.
func (f F2) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(f), 'f', 2, 64)), nil
}

// Candidate is one possible transmitting station with its identification score.
type Candidate struct {
	StationID int    `json:"id"`
	Label     string `json:"label"`
	Score     F2     `json:"score"`
}

// Line is one line of a record with its send time and signal quality.
type Line struct {
	Time    time.Time `json:"t"`
	Quality F2        `json:"q"`
	Text    string    `json:"x"`
}

// Record is one received transmission.
type Record struct {
	ID          int64       `json:"id"`
	Version     int         `json:"v"`
	Channel     int         `json:"ch"`            // kHz
	Rcv         int         `json:"rcv,omitempty"` // decoding channel (receiver) that received it: 0 = the first, 1 = the second
	Received    time.Time   `json:"rx"`
	End         time.Time   `json:"end"`
	Kind        string      `json:"kind"` // message, incomplete, fragment
	B1          string      `json:"b1"`
	B2          string      `json:"b2"`
	Serial      string      `json:"ser"`
	NavArea     int         `json:"area"`
	StationID   int         `json:"sid"`
	Station     string      `json:"st"` // station label, "UNID" when unknown
	DistanceKm  float64     `json:"km"`
	BearingDeg  float64     `json:"brg"`
	Quality     F2          `json:"q"`
	Comment     string      `json:"cmt"`
	IdentMethod string      `json:"im"`
	IdentScore  F2          `json:"is"`
	Candidates  []Candidate `json:"cand,omitempty"`
	Manual      bool        `json:"man"`            // B1 / NAVAREA set by the operator: never re-identified
	Picked      int         `json:"pick,omitempty"` // station chosen by the operator among the candidates (ID); 0 = none
	Lines       []Line      `json:"lines"`
	StartFix    string      `json:"sfix,omitempty"` // the damaged ZCZC as received, when the start was recognized by position and structure
	Corrected   string      `json:"cor,omitempty"`  // body after word autocorrection (of the effective text)
	Revs        []Revision  `json:"rev,omitempty"`  // stretches the operator replaced, e.g. text decoded in the wrong shift
}

// Revision replaces Lines[Line].Text[Start:End] (raw text, never changed) with New when the effective text is built.
type Revision struct {
	Line  int    `json:"l"`
	Start int    `json:"s"`
	End   int    `json:"e"`
	Old   string `json:"o"`
	New   string `json:"n"`
}

// Effective returns the text of line i with the operator's revisions applied; each revised stretch is shown as <New>.
func (r Record) Effective(i int) string {
	raw := r.Lines[i].Text
	var rs []Revision
	for _, v := range r.Revs {
		if v.Line == i && v.Start >= 0 && v.End <= len(raw) && v.Start < v.End {
			rs = append(rs, v)
		}
	}
	if len(rs) == 0 {
		return raw
	}
	sort.Slice(rs, func(a, b int) bool { return rs[a].Start < rs[b].Start })
	var b strings.Builder
	at := 0
	for _, v := range rs {
		if v.Start < at {
			continue // overlapping: ignore
		}
		b.WriteString(raw[at:v.Start])
		b.WriteString("<" + v.New + ">")
		at = v.End
	}
	b.WriteString(raw[at:])
	return b.String()
}

// EffectiveLines returns all lines with the revisions applied.
func (r Record) EffectiveLines() []string {
	out := make([]string, len(r.Lines))
	for i := range r.Lines {
		out[i] = r.Effective(i)
	}
	return out
}

// Text returns all lines joined with newlines.
func (r Record) Text() string {
	ts := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		ts[i] = l.Text
	}
	return strings.Join(ts, "\n")
}

// Filter selects records. Zero values mean "any".
type Filter struct {
	Channel      int
	Rcv          int             // 0 = any decoding channel, 1 = the first, 2 = the second
	NavArea      int             // 0 = any
	B1           string          // "" or "*" = any
	NavAreas     map[int]bool    // nil = any; else only these NAVAREAs
	B1s          map[string]bool // nil = any; else only these ID letters
	OmitUnid     bool            // leave out records without a station
	Text         string          // case-insensitive substring of the text
	From, To     time.Time
	Kind         string
	Unidentified bool    // only records without a station
	MaxDistance  float64 // > 0: only records closer than this (km); unidentified records are not matched
	MinDistance  float64 // > 0: only records at least this far (km); unidentified records are not matched
	Limit        int     // 0 = all
	Offset       int
	Newest       bool // newest first (default: oldest first)
}

func (f Filter) match(r *Record) bool {
	switch {
	case f.Channel != 0 && r.Channel != f.Channel:
		return false
	case f.Rcv != 0 && r.Rcv != f.Rcv-1:
		return false
	case f.NavArea != 0 && r.NavArea != f.NavArea:
		return false
	case f.B1 != "" && f.B1 != "*" && r.B1 != f.B1:
		return false
	case f.NavAreas != nil && !f.NavAreas[r.NavArea]:
		return false
	case f.B1s != nil && !f.B1s[r.B1]:
		return false
	case f.OmitUnid && r.StationID == 0:
		return false
	case !f.From.IsZero() && r.Received.Before(f.From):
		return false
	case !f.To.IsZero() && !r.Received.Before(f.To):
		return false
	case f.Kind != "" && r.Kind != f.Kind:
		return false
	case f.Unidentified && r.StationID != 0:
		return false
	case f.MaxDistance > 0 && (r.StationID == 0 || r.DistanceKm >= f.MaxDistance):
		return false
	case f.MinDistance > 0 && (r.StationID == 0 || r.DistanceKm < f.MinDistance):
		return false
	}
	if f.Text != "" {
		q := strings.ToLower(f.Text)
		if !strings.Contains(strings.ToLower(r.Text()), q) && !strings.Contains(strings.ToLower(r.Corrected), q) {
			return false
		}
	}
	return true
}

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record was changed by someone else; reload it")
)

// SyslogEntry is one system log line.
type SyslogEntry struct {
	Time  time.Time `json:"t"`
	Level string    `json:"l"`
	Text  string    `json:"x"`
}

// QPoint is a mean signal quality over one minute.
type QPoint struct {
	Time    time.Time
	Quality float64
}

const (
	SyslogKeep = 10000
	msgFile    = "messages.jsonl"
	logFile    = "syslog.jsonl"
	qualFile   = "quality.csv"
)

// Store is an open store.
type Store struct {
	mu    sync.RWMutex
	dir   string
	recs  []*Record // ordered by Received then ID
	byID  map[int64]*Record
	next  int64
	msgF  *os.File
	trash []TrashBatch // deletions that can be undone, oldest first

	logF     *os.File
	logLines int
	sys      []SyslogEntry

	qualF   *os.File
	qual    []QPoint
	qMinute int64
	qSum    float64
	qN      int
}

type op struct {
	Op string  `json:"op"`
	R  *Record `json:"r,omitempty"`
	ID int64   `json:"id,omitempty"`
}

// Open opens (creating if needed) the store in dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, byID: map[int64]*Record{}, next: 1, qMinute: -1}
	if err := s.loadMessages(); err != nil {
		return nil, err
	}
	if err := s.loadTrash(); err != nil {
		return nil, err
	}
	if err := s.loadSyslog(); err != nil {
		return nil, err
	}
	if err := s.loadQuality(); err != nil {
		return nil, err
	}
	return s, nil
}

func scanLines(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Store) loadMessages() error {
	path := filepath.Join(s.dir, msgFile)
	err := scanLines(path, func(b []byte) error {
		var o op
		if err := json.Unmarshal(b, &o); err != nil {
			return nil // a damaged (e.g. half-written last) line is skipped
		}
		switch o.Op {
		case "put":
			if o.R != nil {
				s.byID[o.R.ID] = o.R
				if o.R.ID >= s.next {
					s.next = o.R.ID + 1
				}
			}
		case "del":
			delete(s.byID, o.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range s.byID {
		s.recs = append(s.recs, r)
	}
	s.sortRecs()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	s.msgF = f
	return nil
}

func (s *Store) sortRecs() {
	sort.SliceStable(s.recs, func(i, j int) bool {
		a, b := s.recs[i], s.recs[j]
		if !a.Received.Equal(b.Received) {
			return a.Received.Before(b.Received)
		}
		return a.ID < b.ID
	})
}

func (s *Store) appendOp(o op) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = s.msgF.Write(append(b, '\n'))
	return err
}

// normalize rounds times to the millisecond and qualities to two decimals, as they are stored.
func normalize(r *Record) {
	r.Received, r.End = r.Received.Round(time.Millisecond), r.End.Round(time.Millisecond)
	r.Quality = F2(math.Round(float64(r.Quality)*100) / 100)
	r.IdentScore = F2(math.Round(float64(r.IdentScore)*100) / 100)
	if len(r.Lines) > 0 {
		ls := make([]Line, len(r.Lines))
		for i, l := range r.Lines {
			l.Time = l.Time.Round(time.Millisecond)
			l.Quality = F2(math.Round(float64(l.Quality)*100) / 100)
			ls[i] = l
		}
		r.Lines = ls
	}
}

// Add stores a new record (its ID and Version are assigned) and returns it.
func (s *Store) Add(r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID, r.Version = s.next, 1
	normalize(&r)
	if r.Station == "" {
		r.Station = "UNID"
	}
	if err := s.appendOp(op{Op: "put", R: &r}); err != nil {
		return Record{}, err
	}
	s.next++
	p := &r
	s.byID[r.ID] = p
	s.recs = append(s.recs, p)
	if n := len(s.recs); n > 1 && s.recs[n-2].Received.After(p.Received) {
		s.sortRecs()
	}
	return r, nil
}

// Get returns a copy of a record.
func (s *Store) Get(id int64) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.byID[id]; ok {
		return *r, true
	}
	return Record{}, false
}

// Update replaces a record. r.Version must equal the stored version (ErrConflict otherwise); the new version is returned.
func (s *Store) Update(r Record) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[r.ID]
	if !ok {
		return Record{}, ErrNotFound
	}
	if old.Version != r.Version {
		return Record{}, ErrConflict
	}
	r.Version++
	normalize(&r)
	if err := s.appendOp(op{Op: "put", R: &r}); err != nil {
		return Record{}, err
	}
	resort := !old.Received.Equal(r.Received)
	*old = r
	if resort {
		s.sortRecs()
	}
	return r, nil
}

// Delete removes records; it returns how many existed.
func (s *Store) Delete(ids ...int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(ids)
}

func (s *Store) deleteLocked(ids []int64) (int, error) {
	if err := s.archive(ids); err != nil { // nothing is deleted that could not be saved first
		return 0, err
	}
	n := 0
	gone := map[int64]bool{}
	for _, id := range ids {
		if _, ok := s.byID[id]; !ok {
			continue
		}
		if err := s.appendOp(op{Op: "del", ID: id}); err != nil {
			return n, err
		}
		delete(s.byID, id)
		gone[id] = true
		n++
	}
	if n > 0 {
		kept := s.recs[:0]
		for _, r := range s.recs {
			if !gone[r.ID] {
				kept = append(kept, r)
			}
		}
		s.recs = kept
	}
	return n, nil
}

// DeleteWhere deletes every record matching f (ignoring Limit, Offset and Newest) and returns the count.
func (s *Store) DeleteWhere(f Filter) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []int64
	for _, r := range s.recs {
		if f.match(r) {
			ids = append(ids, r.ID)
		}
	}
	return s.deleteLocked(ids)
}

// Count returns the number of records.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.recs)
}

// Query returns the matching records (copies) honouring Limit, Offset and Newest, and the total number of matches.
func (s *Store) Query(f Filter) ([]Record, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var idx []int
	for i, r := range s.recs {
		if f.match(r) {
			idx = append(idx, i)
		}
	}
	total := len(idx)
	if f.Newest {
		for i, j := 0, len(idx)-1; i < j; i, j = i+1, j-1 {
			idx[i], idx[j] = idx[j], idx[i]
		}
	}
	if f.Offset > 0 {
		if f.Offset >= len(idx) {
			idx = nil
		} else {
			idx = idx[f.Offset:]
		}
	}
	if f.Limit > 0 && len(idx) > f.Limit {
		idx = idx[:f.Limit]
	}
	out := make([]Record, len(idx))
	for i, k := range idx {
		out[i] = *s.recs[k]
	}
	return out, total
}

// Compact rewrites the message file with only the live records.
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, msgFile)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range s.recs {
		b, _ := json.Marshal(op{Op: "put", R: r})
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.msgF.Close()
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	s.msgF, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	return err
}

// --- system log ---

func (s *Store) loadSyslog() error {
	path := filepath.Join(s.dir, logFile)
	if err := scanLines(path, func(b []byte) error {
		var e SyslogEntry
		if json.Unmarshal(b, &e) == nil {
			s.sys = append(s.sys, e)
		}
		return nil
	}); err != nil {
		return err
	}
	s.logLines = len(s.sys)
	if len(s.sys) > SyslogKeep {
		s.sys = append([]SyslogEntry(nil), s.sys[len(s.sys)-SyslogKeep:]...)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	s.logF = f
	return err
}

// Log appends a system log line (level: info, warn, error).
func (s *Store) Log(level, text string) SyslogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := SyslogEntry{Time: time.Now().UTC().Round(time.Millisecond), Level: level, Text: text}
	b, _ := json.Marshal(e)
	s.logF.Write(append(b, '\n'))
	s.logLines++
	s.sys = append(s.sys, e)
	if len(s.sys) > SyslogKeep {
		s.sys = s.sys[len(s.sys)-SyslogKeep:]
	}
	if s.logLines > 2*SyslogKeep { // rewrite the file with the newest lines only
		path := filepath.Join(s.dir, logFile)
		var sb strings.Builder
		for _, x := range s.sys {
			b, _ := json.Marshal(x)
			sb.Write(b)
			sb.WriteByte('\n')
		}
		s.logF.Close()
		if os.WriteFile(path+".tmp", []byte(sb.String()), 0o644) == nil && os.Rename(path+".tmp", path) == nil {
			s.logLines = len(s.sys)
		}
		s.logF, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	}
	return e
}

// Syslog returns the newest n entries (all if n <= 0), oldest first.
func (s *Store) Syslog(n int) []SyslogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.sys
	if n > 0 && len(src) > n {
		src = src[len(src)-n:]
	}
	return append([]SyslogEntry(nil), src...)
}

// --- quality history ---

func (s *Store) loadQuality() error {
	path := filepath.Join(s.dir, qualFile)
	if err := scanLines(path, func(b []byte) error {
		u, q, ok := strings.Cut(string(b), ",")
		if !ok {
			return nil
		}
		ts, e1 := strconv.ParseInt(u, 10, 64)
		v, e2 := strconv.ParseFloat(q, 64)
		if e1 == nil && e2 == nil {
			s.qual = append(s.qual, QPoint{time.Unix(ts, 0).UTC(), v})
		}
		return nil
	}); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	s.qualF = f
	return err
}

// AddQuality records a quality value; values are averaged per minute and written when the minute changes.
func (s *Store) AddQuality(t time.Time, v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := t.Unix() / 60
	if s.qMinute >= 0 && m != s.qMinute && s.qN > 0 {
		s.flushQualityLocked()
	}
	s.qMinute = m
	s.qSum += v
	s.qN++
}

func (s *Store) flushQualityLocked() {
	if s.qN == 0 {
		return
	}
	p := QPoint{time.Unix(s.qMinute*60, 0).UTC(), s.qSum / float64(s.qN)}
	s.qual = append(s.qual, p)
	fmt.Fprintf(s.qualF, "%d,%.1f\n", p.Time.Unix(), p.Quality)
	s.qSum, s.qN = 0, 0
}

// QualitySince returns the per-minute quality points at or after t.
func (s *Store) QualitySince(t time.Time) []QPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := sort.Search(len(s.qual), func(i int) bool { return !s.qual[i].Time.Before(t) })
	return append([]QPoint(nil), s.qual[i:]...)
}

// Close flushes and closes the files.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushQualityLocked()
	var first error
	for _, f := range []*os.File{s.msgF, s.logF, s.qualF} {
		if f != nil {
			if err := f.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// CountByStation returns how many records each station ID (non-zero) has.
func (s *Store) CountByStation() map[int]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := map[int]int{}
	for _, r := range s.recs {
		if r.StationID != 0 {
			m[r.StationID]++
		}
	}
	return m
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }
