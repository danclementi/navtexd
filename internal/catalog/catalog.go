// Package catalog owns the station table (kept as a YaND-format file in the data directory), identifies the stations of
// received records and supports editing keywords, import, export and re-identification.
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"navtex/internal/geo"
	"navtex/internal/ident"
	"navtex/internal/stations"
	"navtex/internal/store"
)

//go:embed default_stations.txt
var defaultTable []byte

// FileName is the station table inside the data directory.
const FileName = "YAND_STATIONS.txt"

// MaxKeywordChars is the most characters a keyword list (GID or UID) of a station may hold.
const MaxKeywordChars = 300

// Catalog is the station table and identifier.
type Catalog struct {
	mu   sync.RWMutex
	path string
	tab  *stations.Table
	id   *ident.Identifier
	st   *store.Store

	disabled map[string]bool // stations (by key) whose messages are not logged

	// Position returns the operator's position (ok false when not set).
	Position func() (lat, lon float64, ok bool)
}

// Open loads <dir>/YAND_STATIONS.txt; if it is missing the table shipped with the program is written there first.
func Open(dir string, st *store.Store) (*Catalog, error) {
	c := &Catalog{path: filepath.Join(dir, FileName), st: st}
	if _, err := os.Stat(c.path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(c.path, defaultTable, 0o644); err != nil {
			return nil, err
		}
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return nil, err
	}
	tab, err := stations.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.path, err)
	}
	c.set(tab)
	c.loadLogging()
	return c, nil
}

// LoggingFile holds the selective-logging choice in the data directory.
const LoggingFile = "selective_logging.json"

// key identifies a station across table updates.
func key(s stations.Station) string {
	return fmt.Sprintf("%d#%d#%s#%s", s.FreqKHz, s.NavArea, s.ID, s.Callsign)
}

func (c *Catalog) loggingPath() string { return filepath.Join(filepath.Dir(c.path), LoggingFile) }

func (c *Catalog) loadLogging() {
	c.disabled = map[string]bool{}
	var f struct {
		Disabled []string `json:"disabled"`
	}
	if b, err := os.ReadFile(c.loggingPath()); err == nil && json.Unmarshal(b, &f) == nil {
		for _, k := range f.Disabled {
			c.disabled[k] = true
		}
	}
}

// Logged reports whether messages of the station (ID, 0 = unidentified) are to be stored.
func (c *Catalog) Logged(id int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id < 1 || id > len(c.tab.Stations) {
		return true
	}
	return !c.disabled[key(c.tab.Stations[id-1])]
}

// Disabled returns the IDs of the stations that are not logged.
func (c *Catalog) Disabled() []int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []int{}
	for i, s := range c.tab.Stations {
		if c.disabled[key(s)] {
			out = append(out, i+1)
		}
	}
	return out
}

// SetDisabled chooses the stations that are not logged (by ID). Choices for stations that are not in the current table
// are kept.
func (c *Catalog) SetDisabled(ids []int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	inTable := map[string]bool{}
	for _, s := range c.tab.Stations {
		inTable[key(s)] = true
	}
	next := map[string]bool{}
	for k := range c.disabled {
		if !inTable[k] {
			next[k] = true
		}
	}
	for _, id := range ids {
		if id >= 1 && id <= len(c.tab.Stations) {
			next[key(c.tab.Stations[id-1])] = true
		}
	}
	keys := make([]string, 0, len(next))
	for k := range next {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.MarshalIndent(map[string]any{"disabled": keys}, "", "  ")
	if err := os.WriteFile(c.loggingPath(), b, 0o644); err != nil {
		return err
	}
	c.disabled = next
	return nil
}

func (c *Catalog) set(t *stations.Table) {
	c.tab = t
	c.id = ident.New(t)
}

// Label is the station text shown in the Log: "GNI Niton,ENG" (call sign, name, country code).
func Label(s stations.Station) string {
	cs := s.Callsign
	if cs == "___" {
		cs = ""
	}
	return strings.TrimSpace(cs+" "+s.Name) + "," + s.CountryCode
}

// Station returns the station with the given ID (1-based; 0 is "unidentified").
func (c *Catalog) Station(id int) (stations.Station, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id < 1 || id > len(c.tab.Stations) {
		return stations.Station{}, false
	}
	return c.tab.Stations[id-1], true
}

// Stations returns a copy of the table.
func (c *Catalog) Stations() (version string, list []stations.Station) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tab.Version, append([]stations.Station(nil), c.tab.Stations...)
}

func bodyText(r *store.Record) string {
	var sb strings.Builder
	for _, l := range r.Lines {
		if strings.HasPrefix(l.Text, "ZCZC") || l.Text == "NNNN" {
			continue
		}
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Enrich identifies the station of r and sets its station, area, distance and identification fields. A record the
// operator has edited (Manual) keeps its B1 and NAVAREA, and only the station is looked up for them.
func (c *Catalog) Enrich(r *store.Record) {
	counts := map[int]int(nil)
	if c.st != nil {
		counts = c.st.CountByStation()
	}
	c.mu.Lock() // the identifier's History hook is shared
	defer c.mu.Unlock()
	r.Station, r.StationID, r.DistanceKm, r.BearingDeg, r.IdentMethod, r.IdentScore, r.Candidates = "UNID", 0, 0, 0, "", 0, nil
	if r.Kind == "fragment" {
		return
	}
	in := ident.Input{FreqKHz: r.Channel, B1: r.B1, When: r.Received, Text: bodyText(r)}
	if r.Manual {
		in.NavArea = r.NavArea
	}
	if c.Position != nil {
		in.OpLat, in.OpLon, in.HaveOp = c.Position()
	}
	c.id.History = func(i int) int { return counts[i+1] }
	res := c.id.Identify(in)
	for _, cd := range res.Candidates {
		r.Candidates = append(r.Candidates, store.Candidate{StationID: cd.Index + 1, Label: Label(c.tab.Stations[cd.Index]), Score: store.F2(cd.Score)})
	}
	if res.Index < 0 {
		return
	}
	s := c.tab.Stations[res.Index]
	r.Station, r.StationID, r.IdentMethod, r.IdentScore = Label(s), res.Index+1, res.Method, store.F2(res.Score)
	if r.NavArea == 0 || !r.Manual {
		r.NavArea = s.NavArea
	}
	if in.HaveOp {
		r.DistanceKm = math.Round(geo.DistanceKm(in.OpLat, in.OpLon, s.Lat, s.Lon))
		r.BearingDeg = math.Round(geo.BearingDeg(in.OpLat, in.OpLon, s.Lat, s.Lon))
	}
}

func (c *Catalog) save() error {
	var b bytes.Buffer
	if err := c.tab.Write(&b); err != nil {
		return err
	}
	if err := os.WriteFile(c.path+".tmp", b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(c.path+".tmp", c.path)
}

// SetKeywords replaces one keyword list (kind "GID" or "UID") of a station. Blank entries and repeats are dropped; the
// joined list may not exceed MaxKeywordChars characters.
func (c *Catalog) SetKeywords(id int, kind string, list []string) error {
	kind = strings.ToUpper(kind)
	if kind != "GID" && kind != "UID" {
		return fmt.Errorf("keyword type %q: want GID or UID", kind)
	}
	var clean []string
	seen := map[string]bool{}
	for _, k := range list {
		k = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(k, "#", ""), ",", ""))
		if strings.TrimSpace(k) == "" || seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, k)
	}
	if n := len(strings.Join(clean, ",")); n > MaxKeywordChars {
		return fmt.Errorf("the keyword list is %d characters; at most %d are allowed", n, MaxKeywordChars)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	if kind == "GID" {
		c.tab.Stations[id-1].GID = clean
	} else {
		c.tab.Stations[id-1].UID = clean
	}
	c.id = ident.New(c.tab)
	return c.save()
}

// Export returns the table in the YaND file format.
func (c *Catalog) Export() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var b bytes.Buffer
	c.tab.Write(&b)
	return b.Bytes()
}

// Import replaces the table with one read from data in the YaND file format and returns the number of stations.
func (c *Catalog) Import(data []byte) (int, error) {
	tab, err := stations.Parse(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	if len(tab.Stations) == 0 {
		return 0, errors.New("the file contains no stations")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set(tab)
	return len(tab.Stations), c.save()
}

// Reidentify runs the identification again over the stored records (after a table or position change). It returns how
// many records changed.
func (c *Catalog) Reidentify(st *store.Store) (int, error) {
	recs, _ := st.Query(store.Filter{})
	changed := 0
	for _, r := range recs {
		old := r
		c.Enrich(&r)
		if r.StationID == old.StationID && r.NavArea == old.NavArea && r.DistanceKm == old.DistanceKm && r.BearingDeg == old.BearingDeg &&
			r.IdentMethod == old.IdentMethod && r.IdentScore == old.IdentScore {
			continue
		}
		if _, err := st.Update(r); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
