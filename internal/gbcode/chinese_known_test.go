package gbcode

import (
	"strings"
	"testing"
)

// The plain NNNN that follows the full-width one may sit in the same line (Tianjin sends no line break): it is not read as units.
func TestEndMarkAndPlainNNNN(t *testing.T) {
	for in, want := range map[string]string{
		"AHWAHWAHWAIZAIZAIZAIZNNNN":    "１１１ＮＮＮＮ NNNN",
		"AHWAHWAHWAIZAIZAIZAIBNNNN":    "１１１ＮＮＮＮ NNNN", // the fourth N damaged
		"AHWAHWAHWAIZAIZAIZAIZ":        "１１１ＮＮＮＮ",
		"AHWAHWAHWAIZAIZAIZAIZ~~~NNNN": "１１１ＮＮＮＮ NNNN", // noise before the plain one
		"AHWAHWAHWNNNN":                "１１１ NNNN",     // the full-width one was lost
	} {
		got, ok := DecodeChinese([]string{in}, 0)
		if !ok || got[0] != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
		if g, _ := Decode([]string{in}, 0); g[0] != want { // the same for a message that only looks Chinese
			t.Errorf("strict %q -> %q, want %q", in, g, want)
		}
	}
	// the plain NNNN alone in its line (the other stations) stays as it is
	got, _ := DecodeChinese([]string{"AHWAHWAHWAIZAIZAIZAIZ", "NNNN"}, 0)
	if got[0] != "１１１ＮＮＮＮ" || got[1] != "NNNN" {
		t.Errorf("separate lines: %q", got)
	}
}

// A line with a blank that noise made of a letter is read when the text is known to be Chinese, as if the blank were an
// unreadable letter. A line of words stays as it is.
func TestStrayBlank(t *testing.T) {
	line := shanghai["EA23"][5]
	blank := line[:40] + " " + line[41:]
	tilde := line[:40] + "~" + line[41:]
	got, ok := DecodeChinese([]string{blank}, 200)
	want, _ := DecodeChinese([]string{tilde}, 200)
	if !ok || got[0] != want[0] || got[0] == blank {
		t.Errorf("blank line: %q, want %q", got[0], want[0])
	}
	if g, _ := Decode([]string{blank}, 200); g[0] != blank {
		t.Errorf("a stranger's text must not read a line with a blank: %q", g[0])
	}
	for _, l := range []string{"AIZAIZAIZAIZ ", "AIZAIZ~~~K ZA", "AIZAIZA ZAIZ"} {
		if g, _ := DecodeChinese([]string{l}, 0); g[0] == l {
			t.Errorf("%q not read", l)
		}
	}
	for _, w := range []string{"SEA STATE IS HIGH IN THE NORTH", "WARNING TWO DEGREES NORTH OF THE ISLAND"} {
		if g, _ := DecodeChinese([]string{w}, 0); g[0] != w {
			t.Errorf("words read as units: %q -> %q", w, g[0])
		}
	}
}

// Header digits that were read in the letters shift (build 173): EAIY is EA86 and WYQQQIQYQI is 2611181618.
func TestDigitLines(t *testing.T) {
	in := []string{"ZCZC EAIY", "WYQQQIQYQI", "FQKDMMDMMCDBCXAFZT", "AIZAIZAIZAIZ", "NNNN"}
	got, _ := DecodeChinese(in, 0)
	if got[0] != "ZCZC EA86" || got[1] != "2611181618" || strings.HasPrefix(got[2], "FQK") {
		t.Errorf("header: %q", got)
	}
	for _, l := range []string{"ZCZC EA23", "ZCZC SB61", "ZCZC EAWY~", "ZCZC D~EOWQU~"} { // digits already, or damaged: left alone
		g, _ := DecodeChinese([]string{l, "2611181618"}, 0)
		if g[0] != l && l != "ZCZC EAWY~" || g[1] != "2611181618" {
			t.Errorf("%q -> %q", l, g)
		}
	}
	if g, _ := Decode(in, 0); g[0] != "ZCZC EAIY" {
		t.Errorf("a stranger's text keeps its header: %q", g[0])
	}
}
