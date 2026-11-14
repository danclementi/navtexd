// Package presets keeps the numbered filter presets of the Log tab. They are stored in the data folder, so every browser that
// uses the server sees the same ones. A preset holds the filter as the dialog shows it: relative dates and times are kept as
// the words the user chose (today-1, sunset-1h), not as the date they stood for when the preset was saved.
package presets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"navtex/internal/when"
)

// Slots is the number of presets.
const Slots = 10

// FileName is the preset file in the data folder.
const FileName = "navtexd_filter_presets.json"

// MaxBytes is the most a stored preset may take.
const MaxBytes = 4096

// End is one end of the date range: the fixed date and time typed in the boxes, or the relative words (D, S) chosen instead.
// A relative word replaces the fixed text, so only one of each pair is set.
type End struct {
	Date string `json:"date"` // yyyy-mm-dd
	Time string `json:"time"` // hh:mm
	D    string `json:"d"`    // today, today-1 ... today-7 (local only)
	S    string `json:"s"`    // sunset-1h, sunrise+90m ... (local only)
}

// MaxName is the longest preset name (characters).
const MaxName = 24

// Filter is the Log filter, with the preset's name.
type Filter struct {
	Version int    `json:"version"`
	Name    string `json:"name"` // what the Log tab's list calls it; empty is shown as "Preset N"
	Zone    string `json:"zone"` // utc or local
	From    End    `json:"from"`
	To      End    `json:"to"`
	Freq    string `json:"freq"` // the pick lists: empty means all
	Rcv     string `json:"rcv"`
	Area    string `json:"area"`
	B1      string `json:"b1"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	KMin    string `json:"kmin"`
	KMax    string `json:"kmax"`
	Unid    bool   `json:"unid"`
	Omit    bool   `json:"omit"`
}

var pickRE = regexp.MustCompile(`^[A-Za-z0-9.,*?\- ]*$`)

func (e End) validate(name string, local bool) error {
	if e.Date != "" {
		if _, err := when.ParseDate(e.Date); err != nil {
			return fmt.Errorf("%s date: %w", name, err)
		}
	}
	if e.Time != "" {
		if r, err := when.ParseTimeRef(e.Time); err != nil || r.Sun {
			return fmt.Errorf("%s time %q: want hh:mm", name, e.Time)
		}
	}
	if e.D != "" {
		if r, err := when.ParseDateRef(e.D); err != nil || !r.Relative {
			return fmt.Errorf("%s: %q is not a relative date", name, e.D)
		}
		if !local {
			return fmt.Errorf("%s: relative dates are local", name)
		}
	}
	if e.S != "" {
		if r, err := when.ParseTimeRef(e.S); err != nil || !r.Sun {
			return fmt.Errorf("%s: %q is not a sunrise or sunset time", name, e.S)
		}
		if !local {
			return fmt.Errorf("%s: sun times are local", name)
		}
	}
	return nil
}

// Validate checks a filter that came from a browser.
func (f Filter) Validate() error {
	if n := []rune(f.Name); len(n) > MaxName || strings.TrimSpace(f.Name) != f.Name || strings.ContainsFunc(f.Name, unicode.IsControl) {
		return fmt.Errorf("a name has up to %d characters, with no control characters or spaces at the ends", MaxName)
	}
	if f.Version != 1 {
		return fmt.Errorf("unknown preset version %d", f.Version)
	}
	if f.Zone != "utc" && f.Zone != "local" {
		return fmt.Errorf("zone must be utc or local")
	}
	local := f.Zone == "local"
	if err := f.From.validate("from", local); err != nil {
		return err
	}
	if err := f.To.validate("to", local); err != nil {
		return err
	}
	for name, v := range map[string]string{"frequency": f.Freq, "channel": f.Rcv, "NAVAREA": f.Area, "ID letter": f.B1, "kind": f.Kind} {
		if len(v) > 200 || !pickRE.MatchString(v) {
			return fmt.Errorf("bad %s list", name)
		}
	}
	if len([]rune(f.Text)) > 200 {
		return errors.New("body text is too long")
	}
	for name, v := range map[string]string{"minimum distance": f.KMin, "maximum distance": f.KMax} {
		if v == "" {
			continue
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil || n < 0 {
			return fmt.Errorf("bad %s", name)
		}
	}
	if b, _ := json.Marshal(f); len(b) > MaxBytes {
		return errors.New("preset is too large")
	}
	return nil
}

// Store holds the presets (a slot is nil while unused) and keeps them in the data folder.
type Store struct {
	mu    sync.Mutex
	path  string
	slots [Slots]*Filter
}

// Open reads the presets in dir. A missing file is an empty store; a file that cannot be read as presets is moved aside (.bad)
// and the store starts empty, so a damaged file never keeps the program from running.
func Open(dir string) *Store {
	s := &Store{path: filepath.Join(dir, FileName)}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var in struct {
		Slots []*Filter `json:"slots"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		_ = os.Rename(s.path, s.path+".bad")
		return s
	}
	for i := 0; i < Slots && i < len(in.Slots); i++ {
		if f := in.Slots[i]; f != nil && f.Validate() == nil {
			s.slots[i] = f
		}
	}
	return s
}

// All returns a copy of the slots.
func (s *Store) All() []*Filter {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Filter, Slots)
	for i, f := range s.slots {
		if f != nil {
			c := *f
			out[i] = &c
		}
	}
	return out
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(struct {
		Slots []*Filter `json:"slots"`
	}{s.slots[:]}, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(s.path+".tmp", s.path)
}

// Set stores f in slot n (1 to Slots); a nil filter empties the slot.
func (s *Store) Set(n int, f *Filter) error {
	if n < 1 || n > Slots {
		return fmt.Errorf("preset %d: there are %d", n, Slots)
	}
	if f != nil {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f != nil && f.Name != "" {
		// a name is used once (the list on the Log tab must tell the presets apart); "Preset 4" is slot 4's own name
		for i, o := range s.slots {
			if o != nil && i != n-1 && strings.EqualFold(o.Name, f.Name) {
				return fmt.Errorf("the name %q is already used by preset %d", f.Name, i+1)
			}
		}
		if k, ok := strings.CutPrefix(strings.ToLower(f.Name), "preset "); ok {
			if v, err := strconv.Atoi(k); err == nil && v != n {
				return fmt.Errorf("the name %q is what preset %d is called when it has none", f.Name, v)
			}
		}
	}
	old := s.slots[n-1]
	s.slots[n-1] = f
	if err := s.save(); err != nil {
		s.slots[n-1] = old
		return err
	}
	return nil
}
