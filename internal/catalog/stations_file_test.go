package catalog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"navtex/internal/stations"
	"navtex/internal/store"
)

// the table embedded for the program is the one of the user group; data/ keeps a copy of it for the tests of other packages
func TestEmbeddedTableIsTheDataCopy(t *testing.T) {
	b, err := os.ReadFile("../../data/YAND_STATIONS.txt")
	if err != nil {
		t.Skip("data copy not available")
	}
	if !bytes.Equal(b, baseTable) {
		t.Error("data/YAND_STATIONS.txt and internal/catalog/default_stations.txt differ")
	}
}

func TestMergeTables(t *testing.T) {
	base := []byte("VERSION5\r\n1#490#A#EJM#One#IRL#Ireland#1#2#00:00#####\r\n11#518#Q#XSG#Shanghai#CHN#China#1#2#02:40#####kw1#\r\n")
	add := []byte("VERSION5\r\n11#518#q#xsg#Shanghai#CHN#China#1#2#02:40#####kw2#\r\n11#486.0#E#XSG#Shanghai#CHN#China#1#2#00:40#####\r\n")
	got := string(MergeTables(base, add))
	want := "VERSION5\r\n1#490#A#EJM#One#IRL#Ireland#1#2#00:00#####\r\n11#518#q#xsg#Shanghai#CHN#China#1#2#02:40#####kw2#\r\n11#486.0#E#XSG#Shanghai#CHN#China#1#2#00:40#####\r\n"
	if got != want {
		t.Errorf("merged table:\n%q\nwant\n%q", got, want)
	}
}

func TestShippedTableHasTheAddendum(t *testing.T) {
	tab, err := stations.Parse(bytes.NewReader(defaultTable()))
	if err != nil {
		t.Fatal(err)
	}
	n := map[float64]int{}
	for _, s := range tab.Stations {
		n[s.FreqKHz]++
	}
	if n[486] != 7 || n[424] < 5 || n[4209.5] < 14 {
		t.Errorf("rows per frequency: 486 %d, 424 %d, 4209.5 %d", n[486], n[424], n[4209.5])
	}
	base, _ := stations.Parse(bytes.NewReader(baseTable))
	if len(tab.Stations) < len(base.Stations)+25 {
		t.Errorf("the merged table has %d stations, the base %d", len(tab.Stations), len(base.Stations))
	}
}

func TestOpenMovesTheOldFileName(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	mine := append([]byte(nil), baseTable...)
	mine = []byte(strings.Replace(string(mine), "Malin Head", "My Malin Head", 1))
	if err := os.WriteFile(filepath.Join(dir, LegacyFileName), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if c.Notice == "" {
		t.Error("no notice")
	}
	// the table is the shipped one: a changed name is not the operator's to change, so it is ignored
	got, _ := os.ReadFile(filepath.Join(dir, FileName))
	if strings.Contains(string(got), "My Malin Head") || len(c.tab.Stations) < len(strings.Split(string(mine), "\n"))-2 {
		t.Error("the new file has a changed name of a shipped station")
	}
	if ov, ok, _ := readOverlay(filepath.Join(dir, UserFileName)); !ok || len(ov.Stations) != 0 {
		t.Errorf("overlay: %d stations, hidden %d extra %d ok=%v; first: %+v", len(ov.Stations), len(ov.Hidden), len(ov.Extra), ok, ov.Stations)
	}
	if _, err := os.Stat(filepath.Join(dir, LegacyFileName)); err == nil {
		t.Error("the old file is still there")
	}
	if old, _ := os.ReadFile(filepath.Join(dir, LegacyOldName)); !bytes.Equal(old, mine) {
		t.Error("the old file was not kept as .old")
	}
	// the next start changes nothing
	c2, err := Open(dir, st)
	if err != nil || c2.Notice != "" {
		t.Errorf("second start: %v %q", err, c2.Notice)
	}
}

func TestOpenSeedsFromTheShippedTable(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	c, err := Open(dir, st)
	if err != nil || c.Notice != "" {
		t.Fatalf("%v %q", err, c.Notice)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	got, err := stations.Parse(bytes.NewReader(b))
	ship, _ := shipped()
	if err != nil || !reflect.DeepEqual(got.Stations, ship.Stations) {
		t.Error("the file is not the shipped table")
	}
	if ov, ok, _ := readOverlay(filepath.Join(dir, UserFileName)); !ok || len(ov.Stations)+len(ov.Extra)+len(ov.Hidden) != 0 {
		t.Errorf("a new install has changes of the operator: %+v", ov)
	}
}

func TestImportMerge(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	n0 := len(c.tab.Stations)
	a := stationIdx(t, c, 490, "A", "EJM")
	am, _ := c.Station(a)
	file := []byte("VERSION5\r\n" +
		"1#490#A#EJM#Malin Head X#IRL#Ireland#55.363#-7.340#00:00#04:00#08:00#12:00#16:00#20:00#shared,THEIRS#NEWUID#\r\n" +
		"1#490#Z#NEW#Newplace#IRL#Ireland#50#-7#00:20#04:20#08:20#12:20#16:20#20:20#NEWPLACE##\r\n")
	res, err := c.ImportMerge(file)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Changed != 1 || res.Keywords != 3 || res.Backup == "" || res.Stations != n0+1 {
		t.Errorf("merge result %+v (stations were %d, shipped %d)", res, n0, n0)
	}
	got, _ := c.Station(a)
	if strings.Join(got.GID, ",") != strings.Join(append(append([]string(nil), am.GID...), "shared", "THEIRS"), ",") || got.Name != am.Name {
		t.Errorf("keywords were not added to the station, or its fields were changed: %+v", got)
	}
	if last := c.tab.Stations[len(c.tab.Stations)-1]; last.Callsign != "NEW" || !c.own[len(c.own)-1] {
		t.Errorf("the new station is not last and the operator's own: %+v", last)
	}
	// again: nothing to do, no new backup
	if res, err = c.ImportMerge(file); err != nil || res.Added+res.Changed != 0 || res.Backup != "" {
		t.Errorf("second merge %+v %v", res, err)
	}
	// a station in conflict with one in use is left out, and said so
	res, err = c.ImportMerge([]byte("VERSION5\r\n1#490#Z#CLASH#Clash#IRL#Ireland#50#-7#00:20#04:20#08:20#12:20#16:20#20:20#X##\r\n"))
	if err != nil || res.Added != 0 || len(res.Notes) != 1 {
		t.Errorf("conflict: %+v %v", res, err)
	}
	// the file on disk follows
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), "#NEW#Newplace#") {
		t.Error("the table file does not have the new station")
	}
	// a keyword list has no length limit: a list of 60 words (599 characters) takes one more
	var long []string
	for i := 0; i < 60; i++ {
		long = append(long, fmt.Sprintf("KEYWORD%02d", i))
	}
	if err := c.SetKeywords(a, "GID", long); err != nil {
		t.Fatal(err)
	}
	res, err = c.ImportMerge([]byte("VERSION5\r\n1#490#A#EJM#Malin Head#IRL#Ireland#55.363#-7.340#00:00#04:00#08:00#12:00#16:00#20:00#MORE#\r\n"))
	if err != nil || res.Keywords != 1 {
		t.Errorf("long list: %+v %v", res, err)
	}
	// replacing: the table becomes the file's; shipped stations that it lacks are deleted, the others' lists are the file's
	n, _, _, err := c.Import([]byte("VERSION5\r\n1#490#A#EJM#Malin Head#IRL#Ireland#55.363#-7.340#00:00#04:00#08:00#12:00#16:00#20:00#ONLY#THIS#\r\n"))
	if err != nil || n != 1 || len(c.tab.Stations) != n0 {
		t.Errorf("replace: %d stations in use (%d in the list) %v", n, len(c.tab.Stations), err)
	}
	if g, _ := c.Station(a); strings.Join(g.GID, ",") != "ONLY" {
		t.Errorf("the list of the file: %v", g.GID)
	}
}

// a table made by an earlier version gets the stations of the addendum without any import
func TestMergeTheAddendumIntoAnOldTable(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	if err := os.WriteFile(filepath.Join(dir, FileName), baseTable, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	// a table of an earlier version, with none of the addendum's stations, is the shipped table with the operator's changes: the
	// stations of the addendum are there without any import
	ship, _ := shipped()
	if len(c.tab.Stations) != len(ship.Stations) {
		t.Errorf("%d stations, the shipped table has %d", len(c.tab.Stations), len(ship.Stations))
	}
	found := false
	for _, s := range c.tab.Stations {
		if s.FreqKHz == 518 && s.ID == "E" && s.Callsign == "GNI" {
			for _, k := range s.GID {
				found = found || k == "NEWHAVEN"
			}
			if len(s.GID) < 20 {
				t.Errorf("GNI 518 E: %d keywords", len(s.GID))
			}
		}
	}
	if !found {
		t.Error("NEWHAVEN is not at Niton on 518 kHz")
	}
}

// the keywords that the operator added to the rows of the Canadian and US stations are in the shipped table
func TestShippedKeywordsOfTheOperator(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ // frequency, letter, call -> a GID or UID keyword that a fresh table needs
		"490 D VCK": "ANTICOSTI-N", "490 J VCO": "CALEDONIA", "490 V VAR-9": "CHEBOGUE", "518 A NMA": "ATLC OCEAN-GEORGIA-FLORIDA",
		"518 C VCK": "HONGUEDO", "518 E NME": "NEW ENGLAND", "518 F NMF": "FZNT23", "518 G NMG": "GULF_OF_MX", "518 N NMN": "POTOMAC",
		"518 Q VCO": "VCO", "518 R NMR": "JUAN ATLC", "518 U VAR-9": "SCHOODIC",
	}
	found := 0
	for _, s := range c.tab.Stations {
		k := fmt.Sprintf("%g %s %s", s.FreqKHz, s.ID, s.Callsign)
		if w, ok := want[k]; ok {
			for _, x := range append(append([]string(nil), s.GID...), s.UID...) {
				if x == w {
					found++
				}
			}
		}
	}
	if found != len(want) {
		t.Errorf("%d of %d keywords found in the shipped table", found, len(want))
	}
}
