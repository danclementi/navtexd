package catalog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
	if n[486] != 6 || n[424] < 5 || n[4209.5] < 14 {
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
	got, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !bytes.Equal(got, mine) {
		t.Error("the new file is not a copy of the old one")
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
	if !bytes.Equal(b, defaultTable()) {
		t.Error("the file is not the shipped table")
	}
}

func TestImportMerge(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	table := "VERSION5\r\n" +
		"1#490#A#EJM#Malin Head#IRL#Ireland#55.363#-7.340#00:00#04:00#08:00#12:00#16:00#20:00#MINE,SHARED#OWN#\r\n" +
		"1#490#B#OST#Ostend#BEL#Belgium#51.183#2.808#00:10#04:10#08:10#12:10#16:10#20:10#OOSTENDE#OST#\r\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	n0 := len(c.tab.Stations)
	file := []byte("VERSION5\r\n" +
		"1#490#A#EJM#Malin Head X#IRL#Ireland#55.363#-7.340#00:00#04:00#08:00#12:00#16:00#20:00#shared,THEIRS#NEWUID#\r\n" +
		"1#490#C#NEW#Newplace#IRL#Ireland#50#-7#00:20#04:20#08:20#12:20#16:20#20:20#NEWPLACE##\r\n")
	res, err := c.ImportMerge(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Changed != 1 || res.Keywords != 2 || res.Backup == "" || res.Stations != n0+1 {
		t.Errorf("merge result %+v", res)
	}
	a := c.tab.Stations[0]
	if strings.Join(a.GID, ",") != "MINE,SHARED,THEIRS" || strings.Join(a.UID, ",") != "OWN,NEWUID" || a.Name != "Malin Head" {
		t.Errorf("keywords were not added to the station: %+v", a)
	}
	if last := c.tab.Stations[len(c.tab.Stations)-1]; last.Callsign != "NEW" {
		t.Errorf("the new station is not last: %+v", last)
	}
	// again: nothing to do, no new backup
	if res, err = c.ImportMerge(file, false); err != nil || res.Added+res.Changed != 0 || res.Backup != "" {
		t.Errorf("second merge %+v %v", res, err)
	}
	// replace: the file's row takes the place of the table's, own keywords included
	if res, err = c.ImportMerge(file, true); err != nil || res.Added != 0 || res.Changed != 1 {
		t.Errorf("replacing merge %+v %v", res, err)
	}
	if a := c.tab.Stations[0]; a.Name != "Malin Head X" || strings.Join(a.GID, ",") != "shared,THEIRS" {
		t.Errorf("row not replaced: %+v", a)
	}
	// the file on disk follows
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), "#NEW#Newplace#") {
		t.Error("the table file does not have the new station")
	}
	// a keyword list that is full takes no more
	var long []string
	for i := 0; i < 30; i++ { // 30 words of 9 letters: 299 characters with the commas
		long = append(long, fmt.Sprintf("KEYWORD%02d", i))
	}
	if err := c.SetKeywords(2, "GID", long); err != nil {
		t.Fatal(err)
	}
	res, err = c.ImportMerge([]byte("VERSION5\r\n1#490#B#OST#Ostend#BEL#Belgium#51.183#2.808#00:10#04:10#08:10#12:10#16:10#20:10#MORE#\r\n"), false)
	if err != nil || res.Skipped != 1 || res.Keywords != 0 {
		t.Errorf("full list: %+v %v", res, err)
	}
}

// the addendum file of a release gives a table made earlier its stations when it is merged in
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
	n := len(c.tab.Stations)
	res, err := c.ImportMerge(addendumTable, false)
	if err != nil || res.Added != 25 || res.Changed != 0 || len(c.tab.Stations) != n+25 {
		t.Errorf("merge of the addendum: %+v %v (%d stations, were %d)", res, err, len(c.tab.Stations), n)
	}
}
