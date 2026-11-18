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
