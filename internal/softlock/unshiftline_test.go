package softlock

import "testing"

// With UnshiftOnLine a line end returns to the letters shift: the digits of a Chinese header ("ZCZC AA26", the time group)
// come in the figures shift and the text lines after them are all letters. Without it the shift stays until a letters shift.
func TestCombinerUnshiftOnLine(t *testing.T) {
	code := func(r rune) Code {
		for _, x := range traffic {
			if x.letter == r {
				return x.code
			}
		}
		t.Fatalf("no code for %q", r)
		return 0
	}
	fig, cr, lf := controls[3].code, controls[0].code, controls[1].code
	for _, eol := range []bool{false, true} {
		c := newCombiner(eol)
		c.decode(fig)
		if ch, _ := c.decode(code('W')); ch != '2' {
			t.Errorf("eol=%v: W in the figures shift read %q, want 2 (the header digits must survive)", eol, ch)
		}
		c.decode(cr)
		c.decode(lf)
		want := '-'
		if eol {
			want = 'A'
		}
		if ch, _ := c.decode(code('A')); ch != want {
			t.Errorf("eol=%v: A after the line end read %q, want %q", eol, ch, want)
		}
	}
}
