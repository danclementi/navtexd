package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"navtex/internal/stations"
)

// The station table the program uses is made of two parts that have different owners:
//
//   - the table shipped with the program (default_stations.txt with the addendum merged over it), which an upgrade replaces, and
//   - the operator's changes to it, kept in navtexd_user.json (the overlay): keywords the operator added to a shipped station,
//     shipped keywords the operator removed, other fields of a shipped station the operator changed (a transmission time, say),
//     shipped stations the operator removed, and stations of the operator's own.
//
// The effective table (what identification uses) is the shipped table with the overlay applied. It is also written, in the YaND
// file format, to navtexd_stations.txt, for other programs and for editing in a spreadsheet: the overlay records the checksum of
// the file it wrote, and a file that does not have it was changed from outside, so its differences from the shipped table become
// the new overlay (see Catalog.Open and Catalog.ReloadFromDisk). The same happens to the table of a version that had no overlay,
// once, when it is first opened.

// UserFileName is the overlay, inside the data directory.
const UserFileName = "navtexd_user.json"

// Overlay is the content of navtexd_user.json.
type Overlay struct {
	Version  int           `json:"version"`
	FileSHA  string        `json:"file_sha256,omitempty"` // of the station file that was written with this overlay
	Stations []StationEdit `json:"stations,omitempty"`    // changes to shipped stations
	Hidden   []string      `json:"hidden,omitempty"`      // keys of shipped stations that the operator removed
	Extra    []string      `json:"extra,omitempty"`       // the operator's own stations, one table line each
}

// StationEdit is what the operator changed in one shipped station (named by its key, see StationKeys).
type StationEdit struct {
	Key    string   `json:"key"`
	Name   string   `json:"name,omitempty"` // (for the reader of the file)
	AddGID []string `json:"add_gid,omitempty"`
	DelGID []string `json:"del_gid,omitempty"`
	AddUID []string `json:"add_uid,omitempty"`
	DelUID []string `json:"del_uid,omitempty"`
	Row    string   `json:"row,omitempty"` // the 15 fields before the keywords, when any of them differs from the shipped row
}

func (e StationEdit) empty() bool {
	return len(e.AddGID)+len(e.DelGID)+len(e.AddUID)+len(e.DelUID) == 0 && e.Row == ""
}

// Origin says, for a station of the effective table, what is the operator's (the editor shows it).
type Origin struct {
	Extra  bool     `json:"extra,omitempty"`   // a station of the operator's own
	Row    bool     `json:"row,omitempty"`     // fields other than keywords differ from the shipped row
	OwnGID []string `json:"own_gid,omitempty"` // keywords the operator added to a shipped station
	OwnUID []string `json:"own_uid,omitempty"`
	DelGID []string `json:"del_gid,omitempty"` // shipped keywords the operator removed
	DelUID []string `json:"del_uid,omitempty"`
}

// Edits counts the differences from the shipped table of a station (0: it is as shipped).
func (o Origin) Edits() int {
	n := len(o.OwnGID) + len(o.OwnUID) + len(o.DelGID) + len(o.DelUID)
	if o.Row || o.Extra {
		n++
	}
	return n
}

var shippedCache struct {
	once sync.Once
	tab  *stations.Table
	keys []string
}

var baseCache struct {
	once sync.Once
	gid  map[string][]string
	uid  map[string][]string
	row  map[string]stations.Station
}

// baseLists gives the keyword lists that the base table (the community's, without the addendum) has for the station with a key.
func baseLists(key string) (gid, uid []string) {
	baseCache.once.Do(func() {
		baseCache.gid, baseCache.uid, baseCache.row = map[string][]string{}, map[string][]string{}, map[string]stations.Station{}
		if tab, err := stations.Parse(strings.NewReader(string(baseTable))); err == nil {
			keys, _ := StationKeys(tab.Stations)
			for i, k := range keys {
				baseCache.gid[k], baseCache.uid[k], baseCache.row[k] = tab.Stations[i].GID, tab.Stations[i].UID, tab.Stations[i]
			}
		}
	})
	return baseCache.gid[key], baseCache.uid[key]
}

// shipped returns the table shipped with the program and its keys. Callers must not change it.
func shipped() (*stations.Table, []string) {
	shippedCache.once.Do(func() {
		tab, err := stations.Parse(strings.NewReader(string(defaultTable())))
		if err != nil {
			panic("the shipped station table is wrong: " + err.Error())
		}
		shippedCache.tab = tab
		shippedCache.keys, _ = StationKeys(tab.Stations)
	})
	return shippedCache.tab, shippedCache.keys
}

func sumOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fixedSame says whether two stations agree in every field but the keywords.
func fixedSame(a, b stations.Station) bool {
	return a.NavArea == b.NavArea && a.FreqKHz == b.FreqKHz && a.ID == b.ID && a.Callsign == b.Callsign && a.Name == b.Name &&
		a.CountryCode == b.CountryCode && a.Country == b.Country && a.Lat == b.Lat && a.Lon == b.Lon && a.Times == b.Times
}

// rowText writes the fields of a station before its keywords (15 fields, '#' between).
func rowText(s stations.Station) string {
	var b strings.Builder
	(&stations.Table{Version: "VERSION5", Stations: []stations.Station{{NavArea: s.NavArea, FreqKHz: s.FreqKHz, ID: s.ID, Callsign: s.Callsign, Name: s.Name,
		CountryCode: s.CountryCode, Country: s.Country, Lat: s.Lat, Lon: s.Lon, Times: s.Times}}}).Write(&b)
	line := strings.TrimRight(strings.SplitN(b.String(), "\r\n", 3)[1], "\r\n")
	return strings.TrimSuffix(line, "###") // (the keyword fields are empty: two empty fields and the final '#')
}

// parseRow reads a station from its fields before the keywords, or from a whole line.
func parseRow(text string) (stations.Station, error) {
	if strings.Count(text, "#") < 17 {
		text += "##"
	}
	t, err := stations.Parse(strings.NewReader("VERSION5\r\n" + text + "\r\n"))
	if err != nil {
		return stations.Station{}, err
	}
	if len(t.Stations) != 1 {
		return stations.Station{}, fmt.Errorf("not a station line: %q", text)
	}
	return t.Stations[0], nil
}

// onlyIn keeps the keywords of list that are also in of.
func onlyIn(list, of []string) []string {
	var out []string
	for _, k := range list {
		if hasKeyword(of, k) {
			out = append(out, k)
		}
	}
	return out
}

// notIn keeps the keywords of list that are not in of.
func notIn(list, of []string) []string {
	var out []string
	for _, k := range list {
		if !hasKeyword(of, k) {
			out = append(out, k)
		}
	}
	return out
}

func hasKeyword(list []string, k string) bool {
	for _, x := range list {
		if strings.EqualFold(x, k) {
			return true
		}
	}
	return false
}

// diffList gives the keywords of want that base lacks (add) and the keywords of base that want lacks (del).
func diffList(base, want []string) (add, del []string) {
	for _, k := range want {
		if strings.TrimSpace(k) != "" && !hasKeyword(base, k) && !hasKeyword(add, k) {
			add = append(add, k)
		}
	}
	for _, k := range base {
		if !hasKeyword(want, k) && !hasKeyword(del, k) {
			del = append(del, k)
		}
	}
	return add, del
}

// applyList is the shipped list without the removed keywords, and with the added ones after it.
func applyList(base, add, del []string) []string {
	var out []string
	for _, k := range base {
		if !hasKeyword(del, k) {
			out = append(out, k)
		}
	}
	for _, k := range add {
		if !hasKeyword(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// derive finds what differs between a table (a station file the operator changed, or the effective table after an edit) and the
// shipped table. first says that the table is of a version that had no overlay. It may lack stations and keywords that were
// shipped after it was made, so a shipped station that it lacks is not taken as removed, and a shipped keyword that it lacks is only
// taken as removed when the base table (which an old table was made from) has it too (the keywords of the addendum are not), and a keyword that the base table has and the shipped table lacks is not taken as the operator's.
func derive(user *stations.Table, first bool) Overlay {
	ship, skeys := shipped()
	byKey := make(map[string]int, len(skeys))
	for i, k := range skeys {
		byKey[k] = i
	}
	ukeys, _ := StationKeys(user.Stations)
	ov := Overlay{Version: 1}
	seen := make([]bool, len(skeys))
	for i, u := range user.Stations {
		j, ok := byKey[ukeys[i]]
		if !ok {
			var b strings.Builder
			(&stations.Table{Version: "VERSION5", Stations: []stations.Station{u}}).Write(&b)
			ov.Extra = append(ov.Extra, strings.TrimRight(strings.SplitN(b.String(), "\r\n", 3)[1], "\r\n"))
			continue
		}
		seen[j] = true
		s := ship.Stations[j]
		e := StationEdit{Key: skeys[j], Name: strings.TrimSpace(s.Callsign + " " + s.Name)}
		e.AddGID, e.DelGID = diffList(s.GID, u.GID)
		e.AddUID, e.DelUID = diffList(s.UID, u.UID)
		if first {
			bg, bu := baseLists(skeys[j])
			e.DelGID, e.DelUID = onlyIn(e.DelGID, bg), onlyIn(e.DelUID, bu)
			e.AddGID, e.AddUID = notIn(e.AddGID, bg), notIn(e.AddUID, bu) // (a keyword of the old base table that a release has dropped is not the operator's)
		}
		if !fixedSame(s, u) {
			baseLists(skeys[j]) // (loads the base table)
			// An old table's row as the base table or an earlier addendum had it is not a change of the operator's: only a row that
			// differs from the base row (and is of a station that the base table has) is taken as one.
			if b, ok := baseCache.row[skeys[j]]; !first || ok && !fixedSame(b, u) {
				e.Row = rowText(u)
			}
		}
		if !e.empty() {
			ov.Stations = append(ov.Stations, e)
		}
	}
	if !first {
		for j, k := range skeys {
			if !seen[j] {
				ov.Hidden = append(ov.Hidden, k)
			}
		}
	}
	return ov
}

// build makes the effective table: the shipped table with the overlay applied, and the operator's own stations after it.
func build(ov Overlay) *stations.Table {
	ship, skeys := shipped()
	edit := make(map[string]StationEdit, len(ov.Stations))
	for _, e := range ov.Stations {
		edit[e.Key] = e
	}
	hidden := make(map[string]bool, len(ov.Hidden))
	for _, k := range ov.Hidden {
		hidden[k] = true
	}
	t := &stations.Table{Version: ship.Version}
	for i, s := range ship.Stations {
		if hidden[skeys[i]] {
			continue
		}
		s.GID, s.UID = append([]string(nil), s.GID...), append([]string(nil), s.UID...)
		if e, ok := edit[skeys[i]]; ok {
			if e.Row != "" {
				if r, err := parseRow(e.Row); err == nil {
					r.GID, r.UID = s.GID, s.UID
					s = r
				}
			}
			s.GID = applyList(s.GID, e.AddGID, e.DelGID)
			s.UID = applyList(s.UID, e.AddUID, e.DelUID)
		}
		t.Stations = append(t.Stations, s)
	}
	for _, line := range ov.Extra {
		if s, err := parseRow(line); err == nil {
			t.Stations = append(t.Stations, s)
		}
	}
	return t
}

// origins gives, for each station of the effective table (in its order), what the operator changed in it.
func origins(ov Overlay) []Origin {
	ship, skeys := shipped()
	edit := make(map[string]StationEdit, len(ov.Stations))
	for _, e := range ov.Stations {
		edit[e.Key] = e
	}
	hidden := make(map[string]bool, len(ov.Hidden))
	for _, k := range ov.Hidden {
		hidden[k] = true
	}
	var out []Origin
	for i := range ship.Stations {
		if hidden[skeys[i]] {
			continue
		}
		e := edit[skeys[i]]
		out = append(out, Origin{Row: e.Row != "", OwnGID: e.AddGID, OwnUID: e.AddUID, DelGID: e.DelGID, DelUID: e.DelUID})
	}
	for range ov.Extra {
		out = append(out, Origin{Extra: true})
	}
	return out
}

// shippedLists gives the shipped keyword lists of the station with the given key (nil, nil when the station is not shipped).
func shippedLists(key string) (gid, uid []string, ok bool) {
	ship, skeys := shipped()
	for i, k := range skeys {
		if k == key {
			return ship.Stations[i].GID, ship.Stations[i].UID, true
		}
	}
	return nil, nil, false
}

func readOverlay(path string) (Overlay, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Overlay{}, false, nil
	}
	if err != nil {
		return Overlay{}, false, err
	}
	var ov Overlay
	if err := json.Unmarshal(b, &ov); err != nil {
		return Overlay{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return ov, true, nil
}

func writeOverlay(path string, ov Overlay) error {
	b, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// StationFields are what the operator may change in a station: not the fields that make up its key (frequency, NAVAREA, B1 letter,
// call sign), because the records of the log name the station by that key.
type StationFields struct {
	Name, CountryCode, Country string
	Lat, Lon                   float64
	Times                      [6]string
}

// Validate says what is wrong with the fields (nil: nothing).
func (f StationFields) Validate() error {
	for _, v := range []string{f.Name, f.CountryCode, f.Country} {
		if strings.ContainsAny(v, "#\r\n") {
			return fmt.Errorf("%q: a name may not have a # or a line end", v)
		}
	}
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("the station needs a name")
	}
	if f.Lat < -90 || f.Lat > 90 || f.Lon < -180 || f.Lon > 180 {
		return fmt.Errorf("position %v, %v is not on the earth", f.Lat, f.Lon)
	}
	slots := 0
	for _, t := range f.Times {
		if t == "" {
			continue
		}
		if _, ok := stations.ParseClock(t); !ok {
			return fmt.Errorf("time %q: want hh:mm (UTC)", t)
		}
		slots++
	}
	if slots == 0 {
		return fmt.Errorf("the station needs at least one transmission time")
	}
	return nil
}

func (f StationFields) apply(s *stations.Station) {
	s.Name, s.CountryCode, s.Country, s.Lat, s.Lon, s.Times = strings.TrimSpace(f.Name), strings.TrimSpace(f.CountryCode), strings.TrimSpace(f.Country), f.Lat, f.Lon, f.Times
}

// HiddenStation is a shipped station that the operator removed.
type HiddenStation struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// copyTable gives a table the caller may change.
func (c *Catalog) copyTable() *stations.Table {
	return &stations.Table{Version: c.tab.Version, Stations: append([]stations.Station(nil), c.tab.Stations...)}
}

// SetStation changes the fields of a station (name, country, position, transmission times). For a shipped station the changes are
// kept in the overlay as a changed row; fields that are as shipped again leave no trace.
func (c *Catalog) SetStation(id int, f StationFields) error {
	if err := f.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	t := c.copyTable()
	f.apply(&t.Stations[id-1])
	return c.adopt(t)
}

// RevertStation gives a shipped station the fields it is shipped with (the keywords are not touched).
func (c *Catalog) RevertStation(id int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	ship, skeys := shipped()
	for i, k := range skeys {
		if k == c.keys[id-1] {
			t := c.copyTable()
			s := ship.Stations[i]
			s.GID, s.UID = t.Stations[id-1].GID, t.Stations[id-1].UID
			t.Stations[id-1] = s
			return c.adopt(t)
		}
	}
	return errors.New("that station is not in the table shipped with the program")
}

// AddStation adds a station of the operator's own, with no keywords yet, and returns its place in the table. A station that has
// the key of one in the table is refused.
func (c *Catalog) AddStation(s stations.Station) (int, error) {
	f := StationFields{Name: s.Name, CountryCode: s.CountryCode, Country: s.Country, Lat: s.Lat, Lon: s.Lon, Times: s.Times}
	if err := f.Validate(); err != nil {
		return 0, err
	}
	s.Name, s.CountryCode, s.Country = strings.TrimSpace(s.Name), strings.TrimSpace(s.CountryCode), strings.TrimSpace(s.Country)
	s.ID, s.Callsign = strings.TrimSpace(s.ID), strings.TrimSpace(s.Callsign)
	if s.ID == "" || len(s.ID) != 1 || s.Callsign == "" || strings.ContainsAny(s.ID+s.Callsign, "#\r\n ") {
		return 0, errors.New("the station needs a B1 letter and a call sign (___ if it has none), without blanks")
	}
	if s.NavArea < 1 || s.NavArea > 21 {
		return 0, fmt.Errorf("NAVAREA %d: want 1 to 21", s.NavArea)
	}
	if s.FreqKHz != 490 && s.FreqKHz != 518 && s.FreqKHz != 486 && s.FreqKHz != 424 && s.FreqKHz != 4209.5 {
		return 0, fmt.Errorf("frequency %v: want 490, 518, 486, 424 or 4209.5", s.FreqKHz)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range c.keys {
		if k == key(s) || strings.HasPrefix(k, key(s)+"#") {
			return 0, fmt.Errorf("the table has a station with that frequency, NAVAREA, B1 letter and call sign (%s)", key(s))
		}
	}
	t := c.copyTable()
	t.Stations = append(t.Stations, s)
	if err := c.adopt(t); err != nil {
		return 0, err
	}
	if _, err := c.resync(); err != nil {
		return 0, err
	}
	for i, k := range c.keys {
		if k == key(s) {
			return i + 1, nil
		}
	}
	return len(c.tab.Stations), nil
}

// RemoveStation takes a station out of the table: a shipped one is hidden (RestoreStation brings it back), one of the operator's
// own is deleted. Its records keep their label and name no station.
func (c *Catalog) RemoveStation(id int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	if len(c.tab.Stations) == 1 {
		return errors.New("the last station of the table cannot be removed")
	}
	t := c.copyTable()
	t.Stations = append(t.Stations[:id-1], t.Stations[id:]...)
	if err := c.adopt(t); err != nil {
		return err
	}
	_, err := c.resync()
	return err
}

// Hidden lists the shipped stations that the operator removed.
func (c *Catalog) Hidden() []HiddenStation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ship, skeys := shipped()
	var out []HiddenStation
	for _, k := range c.ov.Hidden {
		for i, sk := range skeys {
			if sk == k {
				out = append(out, HiddenStation{Key: k, Label: fmt.Sprintf("%s kHz %s %s", stations.FreqString(ship.Stations[i].FreqKHz), ship.Stations[i].ID, Label(ship.Stations[i]))})
			}
		}
	}
	return out
}

// RestoreStation puts a removed shipped station back (with the changes the operator had made to it, if any).
func (c *Catalog) RestoreStation(k string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var hidden []string
	found := false
	for _, h := range c.ov.Hidden {
		if h == k {
			found = true
		} else {
			hidden = append(hidden, h)
		}
	}
	if !found {
		return errors.New("that station was not removed")
	}
	c.ov.Hidden = hidden
	c.set(build(c.ov))
	if err := c.save(); err != nil {
		return err
	}
	_, err := c.resync()
	return err
}
