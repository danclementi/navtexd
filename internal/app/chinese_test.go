package app

import (
	"strings"
	"testing"

	"navtex/internal/store"
)

// A message of the national service of China is read as Chinese text on 486 kHz, and only there (a test of "looks Chinese" on
// the other channels took a Spanish forecast for it).
func TestCorrectChinese486Only(t *testing.T) {
	lines := []string{"ZCZC EA10", "2611151633", "FQKDMMDMMCDBCXAFZT", "", "",
		"HIODNMEBKAHNAHWAHVAHZAHZAHUAHXAIBAHOCYIDMMAHNAHWAHWAHOHDRAHNAHWAICAHOFNAAHNAH",
		"VAICAHVAHVAHOFSWHKWAHNAHWAIDAHVAHVAHOFSWDOCAHNAHWAHWAHOHDRAHNAHWAIDAHOFNAHKWA", "AIZAIZAIZAIZ", "NNNN"}
	mk := func(ch int) *store.Record {
		rec := &store.Record{Channel: ch}
		for _, l := range lines {
			rec.Lines = append(rec.Lines, store.Line{Text: l})
		}
		return rec
	}
	r := &Runner{}
	for _, ch := range []int{486000} {
		rec := mk(ch)
		notes := r.Correct(rec)
		if !strings.Contains(rec.Corrected, "上海海岸电台") || !strings.Contains(rec.Corrected, "浙航警（１０４４／２６）东海") {
			t.Errorf("channel %d: corrected text %q", ch, rec.Corrected)
		}
		if (ch == 486000) != (len(notes) == 0) {
			t.Errorf("channel %d: notes %v", ch, notes)
		}
	}
}
