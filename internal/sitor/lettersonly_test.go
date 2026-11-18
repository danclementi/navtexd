package sitor

import "testing"

func TestInterpLettersOnly(t *testing.T) {
	var figs, a uint8
	for v, s := range LTRS {
		if s == tokFIGS {
			figs = v
		}
		if s == "A" {
			a = v
		}
	}
	mask := func(v uint8) uint8 { // the pattern that codeValue turns back into v
		for m := 0; m < 128; m++ {
			if codeValue(uint8(m), LSB) == v {
				return uint8(m)
			}
		}
		t.Fatal("no pattern")
		return 0
	}
	for _, only := range []bool{false, true} {
		p := &interp{order: LSB, only: only}
		p.feed(mask(figs), false, Nearest)
		got := p.feed(mask(a), false, Nearest)
		want := "-"
		if only {
			want = "A"
		}
		if got != want {
			t.Errorf("letters only=%v: A after a figures shift read %q, want %q", only, got, want)
		}
	}
}
