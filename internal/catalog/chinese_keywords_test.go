package catalog

import (
	"strings"
	"testing"
	"time"

	"navtex/internal/store"
)

// A message of the national service whose B1 is missing or damaged is identified by the Chinese words of its text, with the usual
// scoring: the warning prefixes of the maritime safety administrations (沪航警 and 浙航警 are Shanghai's, 鲁航警 and 辽航警 Dalian's,
// 冀航警 and 津航警 Tianjin's, 琼航警 Sanya's), the sea areas and the places. Prefixes that two stations use (Dalian and Tianjin
// both send 鲁, 冀 and 津) do not decide between them. The texts are from captures of 18 November 2026.
func TestChineseKeywordsWithoutB1(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	samples := [][3]string{
		{"12:01", "Sanya", "FKIDNMEBKAHXAHZAHVAHUAHXAIBAHRDNMGPTEBKDHVAHNHLTGVNAHUAIZAJDAHVAHWAIDAHO"},
		{"12:44", "Shanghai", "DQFDNMEBKAHNAIEAHZAHZAHUAHXAIBAHODNLHLYGFSAHNAHWAHWAHOHDRAHNAHWAIEAHOFNAGWDDQOGCJFMAFHDCRDFHZGGMGUSFPSESEGDYGKKDIMDNMCRDHNHGXR"},
		{"12:45", "Shanghai", "HIODNMEBKAHNAHWAHVAIAAHXAHUAHXAIBAHOCYIDMMAHNAHWAHWAHOHDRAHNAHXAHVAHOFNAHEPHMPCWWEMDGMSDCJGGYEYTFWJHBXEAPGPTEEJFTNGSIEMOEASHKOFTGFNW"},
		{"13:08", "Dalian", "ENGDNMEBKAHZAICAIBAHUAHXAIBAHRCKEDMMAHRCGMDMMECJAHWAHWAICGEWERJHAZAHWAHWHDRAHWAIBFNAAHVAIDAHVAHVFSWGEWCUBHHVFQHDMMEPDAIDFEWFZTFKGEHKFMIAAC"},
		{"13:16", "Dalian", "EQADNMEBKAIEAIEAHZAHUAHXAIBDRLDMMAHRCTWDHKAHRHEPGJICWWEMDGMSFKTHBXDCJGGYEYTEAPGPTFUZEEOHQOGVYAACDMIGGMCRDCKBFKGHNHGXRCIDFMIAAC"},
		{"15:02", "Tianjin", "DUSDNMEBKAIDAHXAHZAHUAHXAIBAAACKEDMMAAACLTJPHCXCDHKAAAEASHKOGJBDKXCRDCKBFTGFNWDOCFCGETHAAAGKKDIMCRDCKBHNHGXR"},
		{"15:04", "Tianjin", "EAJDNMEBKAHZAHVAICAHUAHXAIBGCJEAJDHKGCJEAJDHKAIZAHYAIADNUCVTDFEGXADSBDFXHJOCNEGKKDIMDNMCRDHNHGXR"},
		{"12:12", "Guangzhou", "FZTGFSDMMGLMAAAAIIAAADAJHDVCVKGYKHAPGNYHBPCYICGMDEGAHXAIDAHSAHYAHYAAADZLHJEDEGAHZAHWAHSAHZAICAAAFUECOTAHWAHVAHSAHXAHVAAADJEELCAIIAAADLICYIAAADAJHDVCVKGYKHAPGNYHBP"},
	}
	day := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	for _, s := range samples {
		hm, _ := time.Parse("15:04", s[0])
		rec := &store.Record{Channel: 486000, Kind: "message", Received: day.Add(time.Duration(hm.Hour())*time.Hour + time.Duration(hm.Minute())*time.Minute)}
		rec.Lines = []store.Line{{Text: "ZCZC ~A26"}, {Text: s[2]}, {Text: "NNNN"}}
		c.Enrich(rec)
		if !strings.Contains(rec.Station, s[1]) || rec.IdentMethod != "GID" && rec.IdentMethod != "UID" {
			t.Errorf("%s: station %q (method %q, score %.2f), want %s", s[0], rec.Station, rec.IdentMethod, float64(rec.IdentScore), s[1])
		}
	}
}

// A Spanish forecast of Santa Marta (490 kHz, B1 K) whose position lines (no blanks, 18 or more letters) once passed the
// "looks Chinese" test was looked up at 486 kHz, where no station has B1 K: it is looked up on its own channel (the stations of
// 490 kHz with B1 K are candidates; Grindavik and Santa Marta share the letter and the slot, and only the position or keywords
// tell them apart).
func TestSpanishForecastNotChinese(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	defer st.Close()
	c, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	rec := &store.Record{Channel: 490000, Kind: "message", B1: "K", Received: time.Date(2026, 11, 19, 1, 46, 0, 0, time.UTC)}
	rec.Lines = []store.Line{{Text: "ZCZC KE19"}, {Text: "AVISO COSTERO"}, {Text: "MAR CARIBE COLOMBIANO/XSX"}, {Text: "NW 16-15.0N 081-40.2W"}, {Text: "SW 10-59.4N 077-59.4W"}, {Text: "NNNN"}}
	c.Enrich(rec)
	if len(rec.Candidates) == 0 {
		t.Error("no candidate at 490 kHz for B1 K")
	}
	// Grindavik and Santa Marta share 490 kHz, K and the slot: the Spanish words and COLOMBIA decide
	rec.Lines = append(rec.Lines[:3], store.Line{Text: "COLOMBIA 316/26"}, rec.Lines[3], rec.Lines[4], rec.Lines[5])
	c.Enrich(rec)
	if !strings.Contains(rec.Station, "Santa Marta") {
		t.Errorf("station %q (method %q), want Santa Marta", rec.Station, rec.IdentMethod)
	}
	for _, s := range c.tab.Stations {
		if s.ID == "C" && s.FreqKHz == 518 && s.Callsign == "HJW" {
			for _, k := range append(append([]string(nil), s.GID...), s.UID...) {
				if k == "AVISO COSTERO" || k == "CARIBE COLOMBIANO" {
					t.Errorf("Santa Marta 518 C has the Spanish keyword %q", k)
				}
			}
		}
	}
	if text, khz := textAndFreq(rec); khz != 490 || strings.Contains(text, "□") {
		t.Errorf("frequency %v, text %q", khz, text)
	}
}
