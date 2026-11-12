package catalog

import (
	"bytes"
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
	if !bytes.HasPrefix(got, mine) { // (the stations of the addendum follow)
		t.Error("the new file does not start with the old one")
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

func TestOpenAddsTheAddendumRowsOnce(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	// a table of an earlier version: the community's table with a row edited by the user, none of the addendum
	mine := []byte(strings.Replace(string(baseTable), "Malin Head", "My Malin Head", 1))
	if err := os.WriteFile(filepath.Join(dir, FileName), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Notice, "6 for 486 kHz") || !strings.Contains(c.Notice, "backups/") {
		t.Errorf("notice %q", c.Notice)
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if !strings.Contains(string(b), "My Malin Head") || !strings.Contains(string(b), "#486#E#XSG#Shanghai#") {
		t.Error("the user's edit or the new rows are missing")
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "backups", "navtexd_stations-*")); len(m) != 1 {
		t.Errorf("backups: %v", m)
	}
	// the user deletes the Shanghai 486 row; it stays deleted
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\r\n") {
		if !strings.Contains(l, "#486#E#XSG#") {
			keep = append(keep, l)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(strings.Join(keep, "\r\n")+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2, err := Open(dir, st)
	if err != nil || c2.Notice != "" {
		t.Fatalf("second start: %v %q", err, c2.Notice)
	}
	if b2, _ := os.ReadFile(filepath.Join(dir, FileName)); strings.Contains(string(b2), "#486#E#XSG#") {
		t.Error("a deleted row came back")
	}
}
