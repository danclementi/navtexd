package gbcode

import (
	"strings"
	"testing"
)

func TestUnit(t *testing.T) {
	for u, want := range map[string]string{"FQK": "上", "DMM": "海", "AHN": "（", "AHO": "）", "AHV": "０", "AIE": "９", "AIZ": "Ｎ", "A~B": Unreadable} {
		if got, _ := unit(u); got != want {
			t.Errorf("unit(%s) = %q, want %q", u, got, want)
		}
	}
}

// the second broadcast of a Shanghai message as received: lines of 200 letters break units, the title is its own line
func TestDecodeShanghai(t *testing.T) {
	lines := []string{
		"ZCZC EA75", "2611120850", "FQKDMMDMMCDBCXAFZT", "", "",
		"HIODNMEBKAHNAHWAHVAHYAHZAHUAHXAIBAHOCYIDMMAHNAHWAHWAHOHDRAHNAHWQAHYAHOFNAHKWAHNAHWAH~ZAHOFNAEUCFNAAHNAHVAIBAHVAHVAHOFSWHKWAHNAHWAIDAHVAHVAHOFSWAHNAJFAJAAJDAIYAAAAIYAIMAIZAIUAIXAIMAHOERJHEPAHNAHXAIEAHS",
		"AHZAHVAHTAHVAHVAIZAAAAHWAHXAHYAHSAHXAHVAHTAHVWAHVAIQAHOAHNAHXAIEAH~A~W~SAHZAHVAHTAHVAHVAIZAAAAHWAHXAHYAHSAHYAIAAHTAHVAHVAIQAHOAHNAHYAHVAHSAHXAHVAHTAHVAHVAIZAAAAHWAHXAHYAHSAHYAIAAHTAHVAHVAIQAHODOCAHNAH",
		"YAHVAHSAHXAHVAHTAHVAHVAIZAAAAHWAHXAHYAHSAHXAHVAHTAHVAHVAIQAHOHMPCWWEMDGMSDCJGGYEYTEAPGPTFUFDNMFKGEHKFMI", "", "",
		"AIZAIZAIZAIZ", "NNNN",
	}
	// make the two long lines the wrap length, as the receiver cuts them
	all := strings.Join(lines[5:8], "")
	lines[5], lines[6], lines[7] = all[:200], all[200:400], all[400:]
	out, changed := Decode(lines, 200)
	if !changed || len(out) != len(lines) {
		t.Fatalf("changed=%v lines %d, want %d", changed, len(out), len(lines))
	}
	if out[0] != "ZCZC EA75" || out[1] != "2611120850" || out[len(out)-1] != "NNNN" {
		t.Errorf("lines that are not units changed: %q", out)
	}
	if out[2] != "上海海岸电台" {
		t.Errorf("title %q", out[2])
	}
	text := strings.Join(out[5:8], "")
	for _, want := range []string{"浙航警（１０３４／２６）东海（１１）月（１３）日", "每日（０６００）时至（１８００）时（ＴＯＲＭ　ＭＡＮＩＬＡ）轮在（２９－４０．００Ｎ　１２３－２０．００Ｅ）", "诸点连线范围内进行试航请宽让"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if out[10] != "ＮＮＮＮ" {
		t.Errorf("end %q", out[10])
	}
}

func TestIsUnitLine(t *testing.T) {
	for l, want := range map[string]bool{"FQKDMM": true, "AB~": true, "NNNN": false, "ZCZC EA75": false, "2611120850": false, "": false, "AB": false} {
		if IsUnitLine(l) != want {
			t.Errorf("IsUnitLine(%q) != %v", l, want)
		}
	}
}
