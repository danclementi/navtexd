// Package store keeps the message log in one SQLite database (messages.db, pure Go driver, no cgo) and the system log and the
// quality history in plain files, all inside one directory. Everything is safe for concurrent use.
package store

import (
	"errors"
	"sort"
	"strconv"
	"strings"
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
	Key       string `json:"key,omitempty"` // the station's own identity (see Record.StationKey)
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
	Channel     int         `json:"ch"`            // carrier frequency in Hz (518000, 4209500); files of earlier versions hold kHz
	Rcv         int         `json:"rcv,omitempty"` // decoding channel (receiver) that received it: 0 = the first, 1 = the second
	Received    time.Time   `json:"rx"`
	End         time.Time   `json:"end"`
	Kind        string      `json:"kind"` // message, incomplete, fragment
	B1          string      `json:"b1"`
	B2          string      `json:"b2"`
	Serial      string      `json:"ser"`
	NavArea     int         `json:"area"`
	StationID   int         `json:"sid"`          // the station's place in the station table now (set when the record is read; see StationKey)
	StationKey  string      `json:"sk,omitempty"` // the station's own identity: what the record really refers to, whatever the order of the table
	Station     string      `json:"st"`           // station label, "UNID" when unknown
	DistanceKm  float64     `json:"km"`
	BearingDeg  float64     `json:"brg"`
	Quality     F2          `json:"q"`
	Comment     string      `json:"cmt"`
	IdentMethod string      `json:"im"`
	IdentScore  F2          `json:"is"`
	Candidates  []Candidate `json:"cand,omitempty"`
	Manual      bool        `json:"man"`            // B1 / NAVAREA set by the operator: never re-identified
	Picked      int         `json:"pick,omitempty"` // station chosen by the operator among the candidates (place in the table); 0 = none
	PickedKey   string      `json:"pk,omitempty"`   // the key of that station
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
	Channel      int             // carrier frequency in Hz (0: any)
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
	dbFile     = "messages.db"
	msgFile    = "messages.jsonl" // the file of earlier versions; read once when the database is created
	logFile    = "syslog.jsonl"
	qualFile   = "quality.csv"
)

// KHz is a carrier frequency (Hz) in kHz (4209.5 for 4209500).
func KHz(hz int) float64 { return float64(hz) / 1000 }

// FormatKHz writes a carrier frequency (Hz) in kHz without superfluous digits: "518", "4209.5".
func FormatKHz(hz int) string { return strconv.FormatFloat(KHz(hz), 'f', -1, 64) }

// legacyKHz converts a frequency that an earlier version stored in kHz to Hz (no NAVTEX carrier is below 100 kHz).
func legacyKHz(r *Record) {
	if r.Channel > 0 && r.Channel < 100000 {
		r.Channel *= 1000
	}
}
