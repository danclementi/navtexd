package softlock

import "testing"

// A figures shift inside text that is all letters (the Chinese service) must not turn the following letters into
// digits and signs; ordinary text still follows the shift.
func TestCombinerLettersOnly(t *testing.T) {
	code := func(r rune) Code {
		for _, x := range traffic {
			if x.letter == r {
				return x.code
			}
		}
		t.Fatalf("no code for %q", r)
		return 0
	}
	fig := controls[3].code // figures shift
	for _, only := range []bool{false, true} {
		c := newCombiner(only)
		if _, ok := c.decode(fig); ok {
			t.Fatal("a shift must print nothing")
		}
		ch, _ := c.decode(code('A'))
		want := '-'
		if only {
			want = 'A'
		}
		if ch != want {
			t.Errorf("letters only=%v: A after a figures shift read %q, want %q", only, ch, want)
		}
	}
}
