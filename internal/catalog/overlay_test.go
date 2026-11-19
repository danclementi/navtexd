package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"navtex/internal/stations"
)

func stationIdx(t *testing.T, c *Catalog, freq float64, id, call string) int {
	t.Helper()
	for i, s := range c.tab.Stations {
		if s.FreqKHz == freq && s.ID == id && s.Callsign == call {
			return i + 1
		}
	}
	t.Fatalf("no station %v %s %s", freq, id, call)
	return 0
}

// What the operator changes in a keyword list is kept apart from what is shipped: additions and removals are in the overlay, the
// station file has the sum, and the shipped list can be had again.
func TestKeywordsOfTheOperatorAreKeptApart(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	n := stationIdx(t, c, 518, "F", "NMF")
	ship, _ := shipped()
	var shipGID []string
	for _, s := range ship.Stations {
		if s.FreqKHz == 518 && s.ID == "F" && s.Callsign == "NMF" {
			shipGID = s.GID
		}
	}
	if len(shipGID) < 3 {
		t.Fatalf("Boston has %d shipped keywords", len(shipGID))
	}
	st, _ := c.Station(n)
	want := append(append([]string(nil), st.GID[1:]...), "MY OWN WORD") // the first shipped keyword goes, one is added
	if err := c.SetKeywords(n, "GID", want); err != nil {
		t.Fatal(err)
	}
	o := c.Origins()[n-1]
	if len(o.OwnGID) != 1 || o.OwnGID[0] != "MY OWN WORD" || len(o.DelGID) != 1 || o.DelGID[0] != shipGID[0] || o.Edits() != 2 {
		t.Fatalf("origin: %+v", o)
	}
	b, _ := os.ReadFile(filepath.Join(dir, UserFileName))
	if !strings.Contains(string(b), "MY OWN WORD") || !strings.Contains(string(b), shipGID[0]) || strings.Count(string(b), `"key"`) != 1 {
		t.Errorf("overlay file: %s", b)
	}
	f, _ := os.ReadFile(filepath.Join(dir, FileName))
	ft, err := stations.Parse(strings.NewReader(string(f)))
	if err != nil || !reflect.DeepEqual(ft.Stations[n-1].GID, want) {
		t.Errorf("the station file is not the sum: %v %v", ft.Stations[n-1].GID, err)
	}
	// a restart changes nothing, and says nothing
	c2, err := Open(dir, nil)
	if err != nil || c2.Notice != "" {
		t.Fatalf("restart: %v %q", err, c2.Notice)
	}
	if o2 := c2.Origins()[n-1]; o2.Edits() != 2 {
		t.Errorf("after the restart: %+v", o2)
	}
	// the shipped list again
	if err := c2.RevertKeywords(n, "GID"); err != nil {
		t.Fatal(err)
	}
	if o3 := c2.Origins()[n-1]; o3.Edits() != 0 {
		t.Errorf("after reverting: %+v", o3)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, UserFileName)); strings.Contains(string(b), "MY OWN WORD") {
		t.Error("the overlay still has the keyword")
	}
	if err := c2.RevertKeywords(len(c2.tab.Stations)+1, "GID"); err == nil {
		t.Error("no station, no revert")
	}
}

// A change made to the station file from outside (a spreadsheet) is taken up as the operator's changes, when the program starts or
// reads the file again; the file as it was is saved first.
func TestStationFileEditedFromOutside(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	text := strings.Replace(string(b), "#00:00#04:00#08:00#12:00#16:00#20:00#", "#00:05#04:05#08:05#12:05#16:05#20:05#", 1) // a changed row (the first station)
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\r\n")
	lines = append(lines[:len(lines)-1], lines[len(lines)-1]+"\r\n1#490#Z#TST#Test#IRL#Ireland#50.000#-7.000#00:50#04:50#08:50#12:50#16:50#20:50#TESTPLACE##") // an own station
	lines = append(lines[:2], lines[3:]...)                                                                                                                    // the second station removed
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n0 := len(c.tab.Stations)
	c2, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Notice == "" || len(c2.tab.Stations) != n0 { // one removed, one added
		t.Errorf("notice %q, %d stations (were %d)", c2.Notice, len(c2.tab.Stations), n0)
	}
	ov, _, _ := readOverlay(filepath.Join(dir, UserFileName))
	if len(ov.Hidden) != 1 || len(ov.Extra) != 1 || len(ov.Stations) != 1 || ov.Stations[0].Row == "" {
		t.Errorf("overlay: hidden %d, extra %d, changed rows %d", len(ov.Hidden), len(ov.Extra), len(ov.Stations))
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "backups", "navtexd_stations-*")); len(m) != 1 {
		t.Errorf("backups: %v", m)
	}
	last := c2.Origins()[len(c2.tab.Stations)-1]
	if !last.Extra {
		t.Errorf("the last station is the operator's own: %+v", last)
	}
	if err := c2.RevertKeywords(len(c2.tab.Stations), "GID"); err == nil {
		t.Error("a station of one's own has no shipped list")
	}
	// and the same reading while the program runs
	c3, _ := Open(dir, nil)
	text3 := strings.Replace(mustRead(t, filepath.Join(dir, FileName)), "TESTPLACE", "TESTPLACE,SECOND", 1)
	os.WriteFile(filepath.Join(dir, FileName), []byte(text3), 0o644)
	r, err := c3.ReloadFromDisk()
	if err != nil || !r.Changed {
		t.Errorf("reload: %+v %v", r, err)
	}
	if s, _ := c3.Station(len(c3.tab.Stations)); len(s.GID) != 2 {
		t.Errorf("own station after the reload: %+v", s.GID)
	}
	if r, err := c3.ReloadFromDisk(); err != nil || r.Changed {
		t.Errorf("a second reload changes nothing: %+v %v", r, err)
	}
}

func mustRead(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A table of an earlier version: what the shipped table gained since (stations, keywords of the addendum, corrected rows) is not
// the operator's removal; what the operator removed from or added to the base table is kept.
func TestMigrationOfAnOlderTable(t *testing.T) {
	dir := t.TempDir()
	tab, err := stations.Parse(strings.NewReader(string(baseTable)))
	if err != nil {
		t.Fatal(err)
	}
	var edited int
	for i, s := range tab.Stations {
		if s.FreqKHz == 518 && s.ID == "F" && s.Callsign == "NMF" {
			tab.Stations[i].GID = append(tab.Stations[i].GID[1:], "MINE")
			edited = i
		}
	}
	var b strings.Builder
	tab.Write(&b)
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ov, _, _ := readOverlay(filepath.Join(dir, UserFileName))
	if len(ov.Hidden) != 0 || len(ov.Extra) != 0 || len(ov.Stations) != 1 || len(ov.Stations[0].AddGID) != 1 || ov.Stations[0].AddGID[0] != "MINE" || len(ov.Stations[0].DelGID) != 1 {
		t.Errorf("overlay: %+v", ov)
	}
	if !strings.Contains(c.Notice, "1 keywords added, 1 shipped keywords removed") {
		t.Errorf("notice: %q", c.Notice)
	}
	_ = edited
	ship, _ := shipped()
	if len(c.tab.Stations) != len(ship.Stations) {
		t.Errorf("%d stations, shipped %d", len(c.tab.Stations), len(ship.Stations))
	}
}
