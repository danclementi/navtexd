package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"

	"navtex/internal/stations"
	"navtex/internal/store"
)

// Reloaded says what reading the station file again changed.
type Reloaded struct {
	Changed    bool     `json:"changed"`    // the file differs from the table that was loaded
	Stations   int      `json:"stations"`   // stations in the file
	Added      int      `json:"added"`      // stations that are new
	Removed    int      `json:"removed"`    // stations that are no longer in the file
	Moved      int      `json:"moved"`      // records whose station changed its place in the table
	Unresolved int      `json:"unresolved"` // records in the log whose station is not in the table
	Warnings   []string `json:"warnings,omitempty"`
}

// ReloadFromDisk reads the station file again, for the case that it was replaced outside the program (edited in a
// spreadsheet or by hand, or copied over). Stations may have been added, removed or moved: the records follow their stations
// by key. A file that cannot be read is refused, and nothing is changed.
func (c *Catalog) ReloadFromDisk() (Reloaded, error) {
	var out Reloaded
	b, err := os.ReadFile(c.path)
	if err != nil {
		return out, err
	}
	tab, err := stations.Parse(bytes.NewReader(b))
	if err != nil {
		return out, fmt.Errorf("%s: %w", c.path, err)
	}
	if len(tab.Stations) == 0 {
		return out, errors.New("the station file contains no stations")
	}
	c.mu.Lock()
	old := map[string]stations.Station{}
	for i, s := range c.tab.Stations {
		old[c.keys[i]] = s
	}
	keys, warns := StationKeys(tab.Stations)
	out.Stations, out.Warnings = len(tab.Stations), warns
	same := len(keys) == len(c.keys)
	seen := map[string]bool{}
	for i, s := range tab.Stations {
		seen[keys[i]] = true
		o, ok := old[keys[i]]
		switch {
		case !ok:
			out.Added++
			same = false
		case i >= len(c.keys) || c.keys[i] != keys[i]:
			same = false
		case !sameKeywords(s.GID, o.GID) || !sameKeywords(s.UID, o.UID) || s.Name != o.Name || s.Lat != o.Lat || s.Lon != o.Lon:
			same = false
		}
	}
	for k := range old {
		if !seen[k] {
			out.Removed++
			same = false
		}
	}
	if same {
		c.mu.Unlock()
		return out, nil
	}
	out.Changed = true
	c.set(tab)
	c.mu.Unlock()
	if out.Moved, err = c.resync(); err != nil {
		return out, err
	}
	if c.st != nil {
		out.Unresolved = c.st.CountUnresolved()
	}
	return out, nil
}

func sameKeywords(a, b []string) bool {
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

// UnidResult says what identifying the unidentified messages again gives (or gave).
type UnidResult struct {
	Checked int            `json:"checked"` // unidentified messages looked at
	Found   int            `json:"found"`   // of these, how many now have a station
	Methods map[string]int `json:"methods"` // by way of identification (UID, GID, B1, ...)
	Top     []StationCount `json:"top"`     // the stations they went to, most first
}

// StationCount is a station label and a number of messages.
type StationCount struct {
	Station string `json:"station"`
	N       int    `json:"n"`
}

// ReidentifyUnid identifies the messages (kind message or incomplete) that have no station again, with the station table as it
// is now. Only those that get a station are changed; anything the operator has edited, and every message that already has a
// station, is left alone. When dry is true nothing is stored. All changes are stored as one action (desc) that Undo takes back.
// freq (kHz, 0 = all) limits it to one channel.
func (c *Catalog) ReidentifyUnid(st *store.Store, freq float64, dry bool, desc string) (UnidResult, error) {
	res := UnidResult{Methods: map[string]int{}}
	recs, _ := st.Query(store.Filter{Unidentified: true, Channel: int(freq*1000 + 0.5)})
	by := map[string]int{}
	var changed []store.Record
	for _, r := range recs {
		if r.Kind != "message" && r.Kind != "incomplete" || r.Manual {
			continue
		}
		res.Checked++
		n := r
		c.Enrich(&n)
		if n.StationID == 0 {
			continue
		}
		res.Found++
		res.Methods[n.IdentMethod]++
		by[n.Station]++
		changed = append(changed, n)
	}
	for k, v := range by {
		res.Top = append(res.Top, StationCount{k, v})
	}
	sort.Slice(res.Top, func(i, j int) bool {
		if res.Top[i].N != res.Top[j].N {
			return res.Top[i].N > res.Top[j].N
		}
		return res.Top[i].Station < res.Top[j].Station
	})
	if dry || len(changed) == 0 {
		return res, nil
	}
	if _, err := st.UpdateManyUndoable(changed, desc); err != nil {
		return res, err
	}
	return res, nil
}
