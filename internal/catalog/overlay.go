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

// Overlay is the content of navtexd_user.json. What the operator may change is limited: the keywords of the stations shipped with
// the program (add, remove), whether a shipped station is in use (a deleted one stays in the list, struck through, and is not
// used), and stations of the operator's own. Everything else about a shipped station (name, country, position, times) is the
// program's, the same for every installation; a file that changes it from outside is not believed.
type Overlay struct {
	Version  int           `json:"version"`
	FileSHA  string        `json:"file_sha256,omitempty"` // of the station file that was written with this overlay
	Stations []StationEdit `json:"stations,omitempty"`    // keyword changes to shipped stations
	Hidden   []string      `json:"hidden,omitempty"`      // keys of shipped stations that the operator deleted
	Extra    []string      `json:"extra,omitempty"`       // the operator's own stations, one table line each
}

// StationEdit is what the operator changed in the keywords of one shipped station (named by its key, see StationKeys).
type StationEdit struct {
	Key    string   `json:"key"`
	Name   string   `json:"name,omitempty"` // (for the reader of the file)
	AddGID []string `json:"add_gid,omitempty"`
	DelGID []string `json:"del_gid,omitempty"`
	AddUID []string `json:"add_uid,omitempty"`
	DelUID []string `json:"del_uid,omitempty"`
}

func (e StationEdit) empty() bool {
	return len(e.AddGID)+len(e.DelGID)+len(e.AddUID)+len(e.DelUID) == 0
}

// Origin says, for a station of the table, what of it is the operator's (the editor shows it).
type Origin struct {
	Own     bool     `json:"own,omitempty"`     // a station of the operator's own
	Deleted bool     `json:"deleted,omitempty"` // a shipped station that the operator deleted: not used
	OwnGID  []string `json:"own_gid,omitempty"` // keywords the operator added to a shipped station
	OwnUID  []string `json:"own_uid,omitempty"`
	DelGID  []string `json:"del_gid,omitempty"` // shipped keywords the operator removed
	DelUID  []string `json:"del_uid,omitempty"`
}

// Edits counts the differences from the shipped table of a station (0: it is as shipped).
func (o Origin) Edits() int {
	n := len(o.OwnGID) + len(o.OwnUID) + len(o.DelGID) + len(o.DelUID)
	if o.Own || o.Deleted {
		n++
	}
	return n
}

// Modified says that a shipped station that is in use has other keywords than it is shipped with.
func (o Origin) Modified() bool {
	return !o.Own && !o.Deleted && len(o.OwnGID)+len(o.OwnUID)+len(o.DelGID)+len(o.DelUID) > 0
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

// lineOf is the table line of a station, with its keywords.
func lineOf(u stations.Station) string {
	var b strings.Builder
	(&stations.Table{Version: "VERSION5", Stations: []stations.Station{u}}).Write(&b)
	return strings.TrimRight(strings.SplitN(b.String(), "\r\n", 3)[1], "\r\n")
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

// triple is what must differ between two stations in use: the frequency, the NAVAREA and the B1 letter.
func triple(s stations.Station) string {
	return fmt.Sprintf("%s#%d#%s", stations.FreqString(s.FreqKHz), s.NavArea, strings.ToUpper(s.ID))
}

// derive finds what a table (a station file that was changed from outside, or the table of a version that had no overlay) changes
// in the shipped table: keywords added to and removed from shipped stations, shipped stations that it lacks (taken as deleted), and
// stations that the shipped table does not have (the operator's own, unless one is in conflict with a station in use, see triple).
// Nothing else is taken: a changed name, position or time of a shipped station is ignored. prev is the overlay that the file came
// from (a station of the file that has the key of a shipped station that prev deleted, and other fields than it, is the operator's
// replacement of it). first says that the table is of a version that had no overlay. It may lack stations and keywords that were
// shipped after it was made, so a shipped station that it lacks is not taken as deleted, and a shipped keyword that it lacks is only
// taken as removed when the base table (which an old table was made from) has it too (the keywords of the addendum are not), and a
// keyword that the base table has and the shipped table lacks is not taken as the operator's. The second result tells what was left out.
func derive(user *stations.Table, first bool, prev Overlay) (Overlay, []string) {
	ship, skeys := shipped()
	byKey := make(map[string]int, len(skeys))
	for i, k := range skeys {
		byKey[k] = i
	}
	prevHidden := map[string]bool{}
	for _, k := range prev.Hidden {
		prevHidden[k] = true
	}
	ukeys, _ := StationKeys(user.Stations)
	ov := Overlay{Version: 1}
	seen := make([]bool, len(skeys))
	var own []stations.Station
	var notes []string
	for i, u := range user.Stations {
		j, ok := byKey[ukeys[i]]
		if ok && prevHidden[skeys[j]] && !fixedSame(ship.Stations[j], u) {
			ok = false // (the operator's own station, with the key of one that was deleted)
		}
		if !ok {
			own = append(own, u)
			continue
		}
		seen[j] = true
		s := ship.Stations[j]
		if !first && !fixedSame(s, u) {
			notes = append(notes, fmt.Sprintf("the file changes more than the keywords of %s; only the keywords are taken", Label(s)))
		}
		e := StationEdit{Key: skeys[j], Name: strings.TrimSpace(s.Callsign + " " + s.Name)}
		e.AddGID, e.DelGID = diffList(s.GID, u.GID)
		e.AddUID, e.DelUID = diffList(s.UID, u.UID)
		if first {
			baseLists(skeys[j]) // (loads the base table)
			bg, bu := baseLists(skeys[j])
			e.DelGID, e.DelUID = onlyIn(e.DelGID, bg), onlyIn(e.DelUID, bu)
			e.AddGID, e.AddUID = notIn(e.AddGID, bg), notIn(e.AddUID, bu) // (a keyword of the old base table that a release has dropped is not the operator's)
		}
		if !e.empty() {
			ov.Stations = append(ov.Stations, e)
		}
	}
	inUse := map[string]string{}
	for j, s := range ship.Stations {
		if seen[j] || first {
			inUse[triple(s)] = Label(s)
		}
	}
	if !first {
		for j, k := range skeys {
			if !seen[j] {
				ov.Hidden = append(ov.Hidden, k)
			}
		}
	}
	for _, u := range own {
		if who, bad := inUse[triple(u)]; bad {
			notes = append(notes, fmt.Sprintf("the station %s (%s kHz, NAVAREA %d, B1 %s) was left out: %s is in use with the same frequency, NAVAREA and B1", Label(u), stations.FreqString(u.FreqKHz), u.NavArea, u.ID, who))
			continue
		}
		inUse[triple(u)] = Label(u)
		ov.Extra = append(ov.Extra, lineOf(u))
	}
	return ov, notes
}

// built is the table the program works with: every shipped station (the deleted ones too, flagged, so that they stay in the list
// and the log can name them), then the operator's own stations.
type built struct {
	tab  *stations.Table
	dead []bool   // shipped stations that the operator deleted
	own  []bool   // stations of the operator's own
	keys []string // the key of every station; the key of an own station starts with "U#", so that it is never a shipped station's
}

// build makes the table: the shipped table with the overlay's keyword changes, and the operator's own stations after it.
func build(ov Overlay) built {
	ship, skeys := shipped()
	edit := make(map[string]StationEdit, len(ov.Stations))
	for _, e := range ov.Stations {
		edit[e.Key] = e
	}
	hidden := make(map[string]bool, len(ov.Hidden))
	for _, k := range ov.Hidden {
		hidden[k] = true
	}
	b := built{tab: &stations.Table{Version: ship.Version}}
	for i, s := range ship.Stations {
		s.GID, s.UID = append([]string(nil), s.GID...), append([]string(nil), s.UID...)
		if e, ok := edit[skeys[i]]; ok {
			s.GID = applyList(s.GID, e.AddGID, e.DelGID)
			s.UID = applyList(s.UID, e.AddUID, e.DelUID)
		}
		b.tab.Stations = append(b.tab.Stations, s)
		b.dead = append(b.dead, hidden[skeys[i]])
		b.own = append(b.own, false)
		b.keys = append(b.keys, skeys[i])
	}
	var mine []stations.Station
	for _, line := range ov.Extra {
		if s, err := parseRow(line); err == nil {
			mine = append(mine, s)
		}
	}
	okeys, _ := StationKeys(mine)
	for i, s := range mine {
		b.tab.Stations = append(b.tab.Stations, s)
		b.dead = append(b.dead, false)
		b.own = append(b.own, true)
		b.keys = append(b.keys, "U#"+okeys[i])
	}
	return b
}

// origins gives, for each station of the table (in its order), what the operator changed in it.
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
		e := edit[skeys[i]]
		out = append(out, Origin{Deleted: hidden[skeys[i]], OwnGID: e.AddGID, OwnUID: e.AddUID, DelGID: e.DelGID, DelUID: e.DelUID})
	}
	for range ov.Extra {
		out = append(out, Origin{Own: true})
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

// fixKeys follows the shipped stations that a release re-keyed: when a key of the overlay is no longer shipped and exactly one
// shipped station that the overlay does not name has the same frequency, NAVAREA and B1 letter (and, for keyword changes, the
// same name), the call sign is all that changed, and the operator's changes go to that station. Other keys are kept as they
// are (nothing applies them) and are listed in the notes.
func fixKeys(ov Overlay) (Overlay, []string) {
	ship, skeys := shipped()
	have := map[string]int{}
	for i, k := range skeys {
		have[k] = i
	}
	named := map[string]bool{}
	for _, e := range ov.Stations {
		named[e.Key] = true
	}
	for _, k := range ov.Hidden {
		named[k] = true
	}
	find := func(old, label string) (string, bool) {
		parts := strings.SplitN(old, "#", 4)
		if len(parts) < 4 {
			return "", false
		}
		want := strings.Join(parts[:3], "#")
		found := ""
		for i, st := range ship.Stations {
			if named[skeys[i]] || strings.Join(strings.SplitN(skeys[i], "#", 4)[:3], "#") != want {
				continue
			}
			if label != "" && !strings.HasSuffix(label, st.Name) {
				continue
			}
			if found != "" {
				return "", false
			}
			found = skeys[i]
		}
		return found, found != ""
	}
	var notes []string
	out := ov
	out.Stations = append([]StationEdit(nil), ov.Stations...)
	out.Hidden = append([]string(nil), ov.Hidden...)
	for i, e := range out.Stations {
		if _, ok := have[e.Key]; ok {
			continue
		}
		if nk, ok := find(e.Key, e.Name); ok {
			notes = append(notes, fmt.Sprintf("your keyword changes of %s now belong to %s (its call sign changed)", e.Key, nk))
			out.Stations[i].Key, named[nk] = nk, true
		} else {
			notes = append(notes, fmt.Sprintf("your keyword changes of %s are not applied: the station is no longer in the shipped table", e.Key))
		}
	}
	for i, k := range out.Hidden {
		if _, ok := have[k]; ok {
			continue
		}
		if nk, ok := find(k, ""); ok {
			notes = append(notes, fmt.Sprintf("the deleted station %s is now %s (its call sign changed)", k, nk))
			out.Hidden[i], named[nk] = nk, true
		}
	}
	return out, notes
}

// ---- changes by the operator ----

// activeTable gives the stations in use (the deleted shipped stations left out): what the station file holds.
func (c *Catalog) activeTable() *stations.Table {
	t := &stations.Table{Version: c.tab.Version}
	for i, s := range c.tab.Stations {
		if !c.dead[i] {
			t.Stations = append(t.Stations, s)
		}
	}
	return t
}

// commit makes the overlay the one in force and writes the station file and the overlay.
func (c *Catalog) commit(ov Overlay) error {
	c.ov = ov
	c.set(build(ov))
	return c.save()
}

func (ov Overlay) clone() Overlay {
	o := ov
	o.Stations = append([]StationEdit(nil), ov.Stations...)
	o.Hidden = append([]string(nil), ov.Hidden...)
	o.Extra = append([]string(nil), ov.Extra...)
	return o
}

func (ov *Overlay) edit(key, label string) *StationEdit {
	for i := range ov.Stations {
		if ov.Stations[i].Key == key {
			return &ov.Stations[i]
		}
	}
	ov.Stations = append(ov.Stations, StationEdit{Key: key, Name: label})
	return &ov.Stations[len(ov.Stations)-1]
}

func (ov *Overlay) tidy() {
	out := ov.Stations[:0]
	for _, e := range ov.Stations {
		if !e.empty() {
			out = append(out, e)
		}
	}
	ov.Stations = out
}

func (ov Overlay) isHidden(k string) bool {
	for _, h := range ov.Hidden {
		if h == k {
			return true
		}
	}
	return false
}

// ownIndex gives the place in ov.Extra of the own station with table place i (0-based), or -1.
func (c *Catalog) ownIndex(i int) int {
	if i < 0 || i >= len(c.own) || !c.own[i] {
		return -1
	}
	n := 0
	for j := 0; j < i; j++ {
		if c.own[j] {
			n++
		}
	}
	return n
}

// setKeywords is SetKeywords for a clean list; the caller holds the lock.
func (c *Catalog) setKeywordsLocked(id int, kind string, clean []string) error {
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	i := id - 1
	if c.dead[i] {
		return errors.New("the station is deleted: restore it first")
	}
	ov := c.ov.clone()
	if n := c.ownIndex(i); n >= 0 {
		s, err := parseRow(ov.Extra[n])
		if err != nil {
			return err
		}
		if kind == "GID" {
			s.GID = clean
		} else {
			s.UID = clean
		}
		ov.Extra[n] = lineOf(s)
		return c.commit(ov)
	}
	gid, uid, _ := shippedLists(c.keys[i])
	base := gid
	if kind == "UID" {
		base = uid
	}
	add, del := diffList(base, clean)
	st := c.tab.Stations[i]
	e := ov.edit(c.keys[i], strings.TrimSpace(st.Callsign+" "+st.Name))
	if kind == "GID" {
		e.AddGID, e.DelGID = add, del
	} else {
		e.AddUID, e.DelUID = add, del
	}
	ov.tidy()
	return c.commit(ov)
}

// SetStation changes the fields of a station of the operator's own (not the keys: frequency, NAVAREA, B1, call sign). The
// fields of a shipped station are the program's and are not changed.
func (c *Catalog) SetStation(id int, f StationFields) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	n := c.ownIndex(id - 1)
	if n < 0 {
		return errors.New("the fields of a station shipped with the program cannot be changed; only its keywords")
	}
	if err := f.Validate(); err != nil {
		return err
	}
	ov := c.ov.clone()
	s, err := parseRow(ov.Extra[n])
	if err != nil {
		return err
	}
	f.apply(&s)
	ov.Extra[n] = lineOf(s)
	return c.commit(ov)
}

// RevertStation puts a shipped station back as shipped: its keyword changes are dropped and, when it was deleted, it is in use again
// (and the operator's own stations that have its frequency, NAVAREA and B1 letter are removed, see RestorePlan).
func (c *Catalog) RevertStation(id int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	i := id - 1
	if c.own[i] {
		return errors.New("a station of your own has nothing to go back to; delete it")
	}
	ov := c.ov.clone()
	k := c.keys[i]
	for j, e := range ov.Stations {
		if e.Key == k {
			ov.Stations = append(ov.Stations[:j], ov.Stations[j+1:]...)
			break
		}
	}
	if c.dead[i] {
		ov = c.withoutHidden(ov, i)
	}
	return c.commit(ov)
}

// withoutHidden returns ov with station i (0-based, deleted) in use again, and without the own stations in its way.
func (c *Catalog) withoutHidden(ov Overlay, i int) Overlay {
	k := c.keys[i]
	var h []string
	for _, x := range ov.Hidden {
		if x != k {
			h = append(h, x)
		}
	}
	ov.Hidden = h
	t := triple(c.tab.Stations[i])
	var extra []string
	for _, line := range ov.Extra {
		if s, err := parseRow(line); err == nil && triple(s) == t {
			continue
		}
		extra = append(extra, line)
	}
	ov.Extra = extra
	return ov
}

// RestorePlan names the own stations that restoring station id (1-based; a deleted one) takes out of use.
func (c *Catalog) RestorePlan(id int) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id < 1 || id > len(c.tab.Stations) || !c.dead[id-1] {
		return nil
	}
	t := triple(c.tab.Stations[id-1])
	var out []string
	for i, s := range c.tab.Stations {
		if c.own[i] && triple(s) == t {
			out = append(out, Label(s))
		}
	}
	return out
}

// AddStation adds a station of the operator's own and returns its ID. Its frequency must be one of the program's channels, and its
// frequency, NAVAREA and B1 letter may not be those of a station in use.
func (c *Catalog) AddStation(s stations.Station) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.Callsign = strings.ToUpper(strings.TrimSpace(s.Callsign))
	if s.Callsign == "" {
		s.Callsign = "___"
	}
	s.ID = strings.ToUpper(strings.TrimSpace(s.ID))
	if err := (StationFields{Name: s.Name, CountryCode: s.CountryCode, Country: s.Country, Lat: s.Lat, Lon: s.Lon, Times: s.Times}).Validate(); err != nil {
		return 0, err
	}
	okf := false
	for _, f := range []float64{490, 518, 486, 424, 4209.5} {
		okf = okf || s.FreqKHz == f
	}
	if !okf {
		return 0, errors.New("the frequency must be 490, 518, 486, 424 or 4209.5 kHz")
	}
	if s.NavArea < 1 || s.NavArea > 21 {
		return 0, errors.New("the NAVAREA must be 1 to 21")
	}
	if len(s.ID) != 1 || s.ID[0] < 'A' || s.ID[0] > 'Z' {
		return 0, errors.New("the B1 letter must be one letter, A to Z")
	}
	if strings.ContainsAny(s.Callsign, "# \r\n") {
		return 0, errors.New("the call sign may not have a space or a #")
	}
	for i, o := range c.tab.Stations {
		if !c.dead[i] && triple(o) == triple(s) {
			return 0, fmt.Errorf("%s is in use with the same frequency, NAVAREA and B1 letter", Label(o))
		}
	}
	s.GID, s.UID = nil, nil
	ov := c.ov.clone()
	ov.Extra = append(ov.Extra, lineOf(s))
	if err := c.commit(ov); err != nil {
		return 0, err
	}
	return len(c.tab.Stations), nil
}

// RemoveStation takes a station out of use: a shipped one stays in the list (deleted, not used), one of the operator's own is gone.
func (c *Catalog) RemoveStation(id int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id < 1 || id > len(c.tab.Stations) {
		return fmt.Errorf("no station %d", id)
	}
	i := id - 1
	if c.dead[i] {
		return errors.New("the station is deleted already")
	}
	active := 0
	for _, d := range c.dead {
		if !d {
			active++
		}
	}
	if active <= 1 {
		return errors.New("the last station cannot be deleted")
	}
	ov := c.ov.clone()
	if n := c.ownIndex(i); n >= 0 {
		ov.Extra = append(ov.Extra[:n], ov.Extra[n+1:]...)
	} else {
		ov.Hidden = append(ov.Hidden, c.keys[i])
	}
	if err := c.commit(ov); err != nil {
		return err
	}
	_, err := c.resync()
	return err
}

// RestoreStation puts a deleted shipped station back in use (see RevertStation: the keyword changes are dropped too).
func (c *Catalog) RestoreStation(id int) error {
	if err := c.RevertStation(id); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.resync()
	return err
}

// Changes counts what the operator changed: own stations, deleted shipped stations, shipped stations with other keywords.
func (c *Catalog) Changes() (own, deleted, edited int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i, o := range origins(c.ov) {
		switch {
		case o.Own:
			own++
		case o.Deleted:
			deleted++
		case o.Modified():
			edited++
		}
		_ = i
	}
	return
}

// RevertAll puts the whole table back as shipped. The table as it was is saved first (backups folder); its name is returned.
func (c *Catalog) RevertAll() (backup string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if backup, err = c.backupLocked(); err != nil {
		return "", fmt.Errorf("could not save the current table first: %w", err)
	}
	if err = c.commit(Overlay{Version: 1}); err != nil {
		return backup, err
	}
	_, err = c.resync()
	return backup, err
}
