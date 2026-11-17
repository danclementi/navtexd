package catalog

import (
	"strings"
	"testing"
	"time"

	"navtex/internal/store"
)

// A message of the national service of China is identified by its Chinese text, whatever channel it is stored under: the first line
// of each message is the name of the station.
func TestEnrichChineseText(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	// the shipped table has the station name of Shanghai (E on 486 kHz) as a keyword
	body := []string{
		"FQKDMMDMMCDBCXAFZT", "", "",
		"HIODNMEBKAHNAHWAHVAHZAHZAHUAHXAIBAHOCYIDMMAHNAHWAHWAHOHDRAHNAHWAICAHOFNAAHNAH",
		"VAICAHVAHVAHOFSWHKWAHNAHWAIDAHVAHVAHOFSWDOCAHNAHWAHWAHOHDRAHNAHWAIDAHOFNAHKWA",
		"AIZAIZAIZAIZ",
	}
	for _, ch := range []int{486000, 518000} { // the receiver of a log from another program may be set to the wrong frequency
		rec := &store.Record{Channel: ch, Received: time.Date(2026, 11, 17, 20, 46, 0, 0, time.UTC), Kind: "message"}
		rec.Lines = append(rec.Lines, store.Line{Text: "ZCZC EA10"}, store.Line{Text: "2611151633"})
		for _, l := range body {
			rec.Lines = append(rec.Lines, store.Line{Text: l})
		}
		rec.Lines = append(rec.Lines, store.Line{Text: "NNNN"})
		rec.B1 = "" // the letter is not needed: the name of the station is in the text
		c.Enrich(rec)
		if rec.StationID == 0 || !strings.Contains(rec.Station, "Shanghai") || rec.IdentMethod != "UID" {
			t.Errorf("channel %d: station %q (id %d, method %q), want Shanghai by its name", ch, rec.Station, rec.StationID, rec.IdentMethod)
		}
	}
}
