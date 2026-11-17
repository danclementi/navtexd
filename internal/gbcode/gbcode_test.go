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

func TestIsUnitLineSymbols(t *testing.T) {
	for l, want := range map[string]bool{
		"AHN{}AHO": true, "HIODNM~~~8~{}DRA": true, "AIZAIZAIZAIZ": true, "~~~~": true,
		"ZCZC": false, "NAVAREA XI": false, "12345": false, "A1": false, "NNNN": false, "ABC DEF": false,
	} {
		if IsUnitLine(l) != want {
			t.Errorf("IsUnitLine(%q) != %v", l, want)
		}
	}
}

// the lines are 77 letters, not the 200 of the receiver: they are cut together all the same, so no unit is broken at a line end,
// and the lines with a burst of fading ("{}") are read too
func TestDecodeFixedWidthAndFades(t *testing.T) {
	for _, wrap := range []int{200, 0} {
		out, changed := Decode(shanghai["EA10"], wrap)
		if !changed || len(out) != len(shanghai["EA10"]) {
			t.Fatalf("wrap %d: changed=%v, %d lines", wrap, changed, len(out))
		}
		text := strings.Join(out[5:], "")
		for _, want := range []string{
			"浙航警（１０４４／２６）东海（１１）月（１７）日（０７００）时至（１８００）时和（１１）月（１８）日至（２６）日每日（０６００）时至（１８００）时（ＮＩＳＳＯＳ　ＫＹＰＲＯＳ）轮在（２９－４０．００Ｎ　１２３－２０．００Ｅ）",
			"和（３０－２０．００Ｎ　１２３－２０．００Ｅ）诸点连线范围内进行试航请宽让",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("wrap %d: missing %q in %q", wrap, want, text)
			}
		}
		if !strings.Contains(text, Unreadable) {
			t.Errorf("wrap %d: the fade left no unreadable character: %q", wrap, text)
		}
		if out[2] != "上海海岸电台" {
			t.Errorf("title %q", out[2])
		}
	}
}

// the noise of a fade spells rare hanzi: they must not be shown as text, but the digits around them are kept
func TestDecodeFadeShowsNoNoise(t *testing.T) {
	out, _ := Decode(shanghai["EA23"], 200)
	text := strings.Join(out, "")
	for _, noise := range []string{"躬", "蜂", "喳", "枪", "囤"} {
		if strings.Contains(text, noise) {
			t.Errorf("noise %q shown in %q", noise, text)
		}
	}
	for _, want := range []string{"（１９７／２６）黄海南部（１１）月（１７）日（０９００）时至（１７００）时在（３４－３５．５８Ｎ　１１９－５４．４５Ｅ）", "－５３．９８Ｅ）和（３４－", "５２．７５Ｅ）四点连线海域范围内"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestLooks(t *testing.T) {
	for name, lines := range shanghai {
		if !Looks(lines) {
			t.Errorf("%s is Chinese text", name)
		}
	}
	english := []string{"ZCZC EA23", "2611161458", "NAVAREA XI WARNING 123/26", "SOUTH CHINA SEA", "VESSELS ARE ADVISED TO KEEP CLEAR OF THE AREA", "NNNN"}
	if Looks(english) {
		t.Error("English text looks Chinese")
	}
	// a run of words with every blank lost is not Chinese: its letters do not spell common characters
	if Looks([]string{"ZCZC EA23", "2611161458", "VESSELSAREADVISEDTOKEEPCLEAROFTHEAREAINTHEPOSITIONNEARTHEHARBOUR", "NNNN"}) {
		t.Error("English without blanks looks Chinese")
	}
	if Looks([]string{"ZCZC EA23", "FQKDMMDMMCDBCXAFZT", "NNNN"}) {
		t.Error("a title alone is too little to say")
	}
	if Looks(nil) {
		t.Error("nothing looks Chinese")
	}
}

func TestHasHan(t *testing.T) {
	if !HasHan("上海") || HasHan("ＮＮＮＮ（１２）") || HasHan("ABC") || HasHan("") {
		t.Error("HasHan")
	}
}
