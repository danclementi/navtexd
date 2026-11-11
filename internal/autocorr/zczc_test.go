package autocorr

import "testing"

// The start code (and the end code) decoded in the wrong shift is offered for reversal, with whatever is stuck to it.
func TestReversalOfStartCode(t *testing.T) {
	c := New()
	cases := []struct{ line, want string }{
		{"3--1~7212++:+: /-78", "EAAQ~UWQWZZCZC XAUI"},
		{"+:+:", "ZCZC"},
		{",,,,", "NNNN"},
	}
	for _, k := range cases {
		r := c.Reversals(k.line, DefaultConfig())
		if len(r) != 1 || r[0].Start != 0 || r[0].Text != k.want {
			t.Errorf("%q -> %+v (want %q)", k.line, r, k.want)
		}
	}
	for _, l := range []string{"ZCZC FE01", "1234 5678", "A,B", "WATERS, NEAR"} { // nothing to reverse
		if r := c.Reversals(l, DefaultConfig()); len(r) != 0 {
			t.Errorf("%q -> %+v", l, r)
		}
	}
}
