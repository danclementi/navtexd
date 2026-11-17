// Package schedule keeps the daily schedule of the Options tab: a list of "set this setting to this value at this time of day".
// A time of day is a clock time in UTC or in the receiver's zone, or (local only) an offset from sunrise, sunset or local midnight.
// The entries repeat every day and are stored in the data folder, so they survive a restart and every browser sees the same ones.
// Which settings may be scheduled, and the running of the schedule, belong to the server; this package only knows the entries and
// when they fall.
package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"navtex/internal/when"
)

// FileName is the schedule file in the data folder.
const FileName = "navtexd_schedule.json"

// Limits.
const (
	MaxEntries = 200
	MaxValue   = 300 // characters of a value (the longest comment the settings take)
)

// Entry is one line of the schedule.
type Entry struct {
	ID    int    `json:"id"`
	Param string `json:"param"` // the setting, by its path in the configuration (operator.comment, extra.0.channel.comment)
	Zone  string `json:"zone"`  // utc or local
	At    string `json:"at"`    // hh:mm; local only: sunrise, sunset or midnight with an offset (sunset-1h, midnight+30m)
	Value string `json:"value"` // what the setting becomes
	Off   bool   `json:"off,omitempty"`
}

// Fire is one occurrence of an entry.
type Fire struct {
	T time.Time
	E Entry
}

// Validate checks the entry; the setting itself is the server's to check.
func (e Entry) Validate() error {
	if e.Param == "" {
		return errors.New("no setting is chosen")
	}
	if e.Zone != "utc" && e.Zone != "local" {
		return fmt.Errorf("the times are in utc or local, not %q", e.Zone)
	}
	r, err := when.ParseTimeRef(e.At)
	if err != nil {
		return err
	}
	if e.Zone == "utc" && !r.Fixed() {
		return errors.New("sunrise, sunset and midnight are local times")
	}
	if utf8.RuneCountInString(e.Value) > MaxValue {
		return fmt.Errorf("the value is longer than %d characters", MaxValue)
	}
	if strings.ContainsAny(e.Value, "\r\n") {
		return errors.New("the value is a single line")
	}
	return nil
}

// UsesSun says that the entry needs the receiving position.
func (e Entry) UsesSun() bool {
	r, err := when.ParseTimeRef(e.At)
	return err == nil && r.Sun
}

// at gives the instant of the entry on a calendar date of the zone it is in (UTC dates for a UTC entry).
func (e Entry) at(d when.Date, z when.Zone) (time.Time, bool) {
	r, err := when.ParseTimeRef(e.At)
	if err != nil {
		return time.Time{}, false
	}
	if e.Zone == "utc" {
		z = when.Zone{Loc: time.UTC}
	}
	return z.At(d, r)
}

// Between lists the instants of the entry after from and not after to, oldest first. z is the receiver's zone and position.
func (e Entry) Between(z when.Zone, from, to time.Time) []time.Time {
	loc := z.Loc
	if e.Zone == "utc" || loc == nil {
		loc = time.UTC
	}
	var out []time.Time
	last := when.DateOf(to, loc).AddDays(1)
	for d := when.DateOf(from, loc).AddDays(-1); ; d = d.AddDays(1) {
		if t, ok := e.at(d, z); ok && t.After(from) && !t.After(to) {
			out = append(out, t)
		}
		if d == last {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// Next is the first instant after now (looking three days ahead).
func (e Entry) Next(z when.Zone, now time.Time) (time.Time, bool) {
	if ts := e.Between(z, now, now.Add(72*time.Hour)); len(ts) > 0 {
		return ts[0], true
	}
	return time.Time{}, false
}

// Due lists the occurrences of the enabled entries after from and not after to, in time order (the entry that comes later in the
// list goes later when two fall on the same minute). Nothing older than three days is looked for.
func Due(entries []Entry, z when.Zone, from, to time.Time) []Fire {
	if min := to.Add(-72 * time.Hour); from.Before(min) {
		from = min
	}
	var out []Fire
	for _, e := range entries {
		if e.Off {
			continue
		}
		for _, t := range e.Between(z, from, to) {
			out = append(out, Fire{t, e})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out
}

// Current gives, for each setting, the latest occurrence not after now: what the schedule says the setting is at this moment.
func Current(entries []Entry, z when.Zone, now time.Time) map[string]Fire {
	cur := map[string]Fire{}
	for _, f := range Due(entries, z, now.Add(-72*time.Hour), now) {
		cur[f.E.Param] = f // later occurrences replace earlier ones
	}
	return cur
}

// Store keeps the entries in the data folder.
type Store struct {
	mu      sync.Mutex
	path    string
	entries []Entry
}

// Open reads the schedule in dir. A missing file is an empty schedule; one that cannot be read is moved aside (.bad), so a damaged
// file never keeps the program from running.
func Open(dir string) *Store {
	s := &Store{path: filepath.Join(dir, FileName)}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var in struct {
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		_ = os.Rename(s.path, s.path+".bad")
		return s
	}
	for _, e := range in.Entries {
		if e.Validate() == nil && len(s.entries) < MaxEntries {
			s.entries = append(s.entries, e)
		}
	}
	return s
}

// All returns a copy of the entries.
func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// Replace stores a new list. Entries without an ID (0) get one; an ID is never used twice.
func (s *Store) Replace(in []Entry) error {
	if len(in) > MaxEntries {
		return fmt.Errorf("at most %d entries", MaxEntries)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen, next := map[int]bool{}, 1
	for _, e := range in {
		if e.ID >= next {
			next = e.ID + 1
		}
	}
	out := make([]Entry, 0, len(in))
	for i, e := range in {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
		if e.ID <= 0 || seen[e.ID] {
			e.ID = next
			next++
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	b, err := json.MarshalIndent(struct {
		Entries []Entry `json:"entries"`
	}{out}, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path+".tmp", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(s.path+".tmp", s.path); err != nil {
		return err
	}
	s.entries = out
	return nil
}
