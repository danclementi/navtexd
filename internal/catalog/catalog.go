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
	"sync/atomic"

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

	keys  []string                       // the key of every station of the table (see StationKeys)
	index atomic.Pointer[map[string]int] // key -> the station's place in the table (1-based); read without a lock by the store
	warns []string                       // what is odd about the table (stations that cannot be told apart)

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
	if st != nil {
		st.SetResolver(c.resolve)
		if _, err := st.BackfillStationKeys(c.keyFor); err != nil {
			return nil, err
		}
		if _, err := st.ResyncStations(c.resolve); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// LoggingFile holds the selective-logging choice in the data directory.
const LoggingFile = "selective_logging.json"

// key identifies a station across table updates.
func key(s stations.Station) string {
	return fmt.Sprintf("%s#%d#%s#%s", stations.FreqString(s.FreqKHz), s.NavArea, s.ID, s.Callsign)
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
	c.keys, c.warns = StationKeys(t.Stations)
	idx := make(map[string]int, len(c.keys))
	for i, k := range c.keys {
		idx[k] = i + 1
	}
	c.index.Store(&idx)
}

// StationKeys gives every station of a table its key: what the station is, whatever its place in the table. A record stores
// the key, so that rows can be moved, added or removed (in a spreadsheet, say) without the log pointing at the wrong
// station. The key is frequency, NAVAREA, B1 letter and call sign. Stations that would share a key (two rows with the call
// sign "___", say) are told apart by their names, and then by their order; the second result says which.
func StationKeys(list []stations.Station) (keys, warnings []string) {
	count := map[string]int{}
	for _, s := range list {
		count[key(s)]++
	}
	used := map[string]bool{}
	for i, s := range list {
		k := key(s)
		if count[k] > 1 {
			k += "#" + strings.TrimSpace(s.Name)
		}
		if used[k] {
			for n := 2; ; n++ {
				if k2 := fmt.Sprintf("%s#%d", k, n); !used[k2] {
					warnings = append(warnings, fmt.Sprintf("station %d (%s %s) cannot be told apart from an earlier station: change its call sign or name", i+1, key(s), s.Name))
					k = k2
					break
				}
			}
		}
		used[k] = true
		keys = append(keys, k)
	}
	return keys, warnings
}

// resolve gives the place (1-based) in the table of the station with the given key.
func (c *Catalog) resolve(k string) (int, bool) {
	p := c.index.Load()
	if p == nil {
		return 0, false
	}
	id, ok := (*p)[k]
	return id, ok
}

// keyOf gives the key of the station at the given place (1-based; "" when there is none).
func (c *Catalog) keyOf(id int) string {
	if id < 1 || id > len(c.keys) {
		return ""
	}
	return c.keys[id-1]
}

// keyFor gives the key of the station a record of an earlier version names by its place: the station at that place if its
// label (when given) and channel agree, else the station of the table with that label on the channel ("" when there is none).
func (c *Catalog) keyFor(id int, label string, channel int) string {
	if id >= 1 && id <= len(c.tab.Stations) {
		if s := c.tab.Stations[id-1]; s.Hz() == channel && (label == "" || Label(s) == label) {
			return c.keys[id-1]
		}
	}
	if label == "" {
		return ""
	}
	for i, s := range c.tab.Stations {
		if s.Hz() == channel && Label(s) == label {
			return c.keys[i]
		}
	}
	return ""
}

// Warnings lists what is odd about the table (stations that cannot be told apart).
func (c *Catalog) Warnings() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.warns...)
}

// Resync brings the records' places in the table in line with their stations' keys (after an import, say).
func (c *Catalog) Resync() (int, error) { return c.resync() }

// resync brings the records' places in line with the table (call it after the table changed, without holding c.mu).
func (c *Catalog) resync() (int, error) {
	if c.st == nil {
		return 0, nil
	}
	return c.st.ResyncStations(c.resolve)
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
	for i, l := range r.Lines {
		if strings.HasPrefix(l.Text, "ZCZC") || l.Text == "NNNN" || i == 0 && r.StartFix != "" {
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
	r.StationKey, r.PickedKey = "", c.keyOf(r.Picked)
	place := func(s stations.Station, idx int, method string, score float64) {
		r.Station, r.StationID, r.IdentMethod, r.IdentScore = Label(s), idx+1, method, store.F2(score)
		r.StationKey = c.keyOf(idx + 1)
		if r.NavArea == 0 || !r.Manual {
			r.NavArea = s.NavArea
		}
		if lat, lon, ok := c.opPos(); ok {
			r.DistanceKm = math.Round(geo.DistanceKm(lat, lon, s.Lat, s.Lon))
			r.BearingDeg = math.Round(geo.BearingDeg(lat, lon, s.Lat, s.Lon))
		}
	}
	if r.Kind == "fragment" {
		// only the operator can give a fragment a station: a "forced ID"
		if r.Picked >= 1 && r.Picked <= len(c.tab.Stations) && c.tab.Stations[r.Picked-1].Hz() == r.Channel {
			place(c.tab.Stations[r.Picked-1], r.Picked-1, "forced ID", 0)
		}
		return
	}
	in := ident.Input{FreqKHz: float64(r.Channel) / 1000, B1: r.B1, When: r.Received, Text: bodyText(r)}
	if r.Manual {
		in.NavArea = r.NavArea
	}
	if c.Position != nil {
		in.OpLat, in.OpLon, in.HaveOp = c.Position()
	}
	c.id.History = func(i int) int { return counts[i+1] }
	res := c.id.Identify(in)
	for _, cd := range res.Candidates {
		r.Candidates = append(r.Candidates, store.Candidate{StationID: cd.Index + 1, Key: c.keyOf(cd.Index + 1), Label: Label(c.tab.Stations[cd.Index]), Score: store.F2(cd.Score)})
	}
	if r.Picked >= 1 && r.Picked <= len(c.tab.Stations) && c.tab.Stations[r.Picked-1].Hz() == r.Channel {
		// the operator chose this station among the candidates (needed when several share frequency, B1 and NAVAREA)
		res.Index, res.Method, res.Score = r.Picked-1, "forced ID", 0 // not among the candidates: a forced ID
		for _, cd := range res.Candidates {
			if cd.Index == res.Index {
				res.Method, res.Score = "manual", cd.Score
			}
		}
	}
	if res.Index < 0 {
		return
	}
	place(c.tab.Stations[res.Index], res.Index, res.Method, res.Score)
}

// Placer returns a function that gives a record made by another program (the log of YaND) its place in this station table: when
// the record's station label and frequency are those of a station of the table, the station's number, its NAVAREA when the record
// has none, and the distance and bearing from the operator's position are set. Other records stay as they are.
func (c *Catalog) Placer() func(r *store.Record) {
	c.mu.RLock()
	byLabel := map[string]int{}
	for i, s := range c.tab.Stations {
		byLabel[fmt.Sprintf("%d|%s", s.Hz(), Label(s))] = i
	}
	list := c.tab.Stations
	keys := append([]string(nil), c.keys...)
	c.mu.RUnlock()
	lat, lon, haveOp := c.opPos()
	return func(r *store.Record) {
		i, ok := byLabel[fmt.Sprintf("%d|%s", r.Channel, r.Station)]
		if !ok {
			if r.StationKey == "" {
				r.StationID = 0 // (a place in the table of another program or version means nothing here)
			}
			return
		}
		s := list[i]
		r.StationID, r.StationKey = i+1, keys[i]
		if r.NavArea == 0 {
			r.NavArea = s.NavArea
		}
		if haveOp {
			r.DistanceKm = math.Round(geo.DistanceKm(lat, lon, s.Lat, s.Lon))
			r.BearingDeg = math.Round(geo.BearingDeg(lat, lon, s.Lat, s.Lon))
		}
	}
}

func (c *Catalog) opPos() (lat, lon float64, ok bool) {
	if c.Position == nil {
		return 0, 0, false
	}
	return c.Position()
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
func (c *Catalog) Import(data []byte) (n int, backup string, err error) {
	tab, err := stations.Parse(bytes.NewReader(data))
	if err != nil {
		return 0, "", err
	}
	if len(tab.Stations) == 0 {
		return 0, "", errors.New("the file contains no stations")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if backup, err = c.backupLocked(); err != nil {
		return 0, "", fmt.Errorf("could not save the current table first: %w", err)
	}
	c.set(tab)
	if err = c.save(); err != nil {
		return len(tab.Stations), backup, err
	}
	_, err = c.resync() // (records follow their stations to their new places)
	return len(tab.Stations), backup, err
}

// backupLocked copies the table file to backups/YAND_STATIONS-<time it was last saved>.TXT and returns that name (empty when
// there is no file yet).
func (c *Catalog) backupLocked() (string, error) {
	fi, err := os.Stat(c.path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(c.path), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "YAND_STATIONS-" + fi.ModTime().UTC().Format("20060102T150405Z") + ".TXT"
	for i := 2; ; i++ { // two imports in the same second: keep both
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("YAND_STATIONS-%s-%d.TXT", fi.ModTime().UTC().Format("20060102T150405Z"), i)
	}
	return name, os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

func sameCandidates(a, b []store.Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
			r.IdentMethod == old.IdentMethod && r.IdentScore == old.IdentScore && sameCandidates(r.Candidates, old.Candidates) {
			continue
		}
		if _, err := st.Update(r); err != nil && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
