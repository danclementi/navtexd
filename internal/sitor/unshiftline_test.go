package sitor

import "testing"

func TestInterpUnshiftOnLine(t *testing.T) {
	find := func(tok string) uint8 {
		for v, s := range LTRS {
			if s == tok {
				return v
			}
		}
		t.Fatalf("no code for %q", tok)
		return 0
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
	figs, cr, lf, w, a := find(tokFIGS), find("\r"), find("\n"), find("W"), find("A")
	for _, eol := range []bool{false, true} {
		p := &interp{order: LSB, eol: eol}
		p.feed(mask(figs), false, Nearest)
		if got := p.feed(mask(w), false, Nearest); got != "2" {
			t.Errorf("eol=%v: W in the figures shift read %q, want 2", eol, got)
		}
		p.feed(mask(cr), false, Nearest)
		p.feed(mask(lf), false, Nearest)
		want := "-"
		if eol {
			want = "A"
		}
		if got := p.feed(mask(a), false, Nearest); got != want {
			t.Errorf("eol=%v: A after the line end read %q, want %q", eol, got, want)
		}
	}
}
