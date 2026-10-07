package sitor

import "testing"

func TestReverse(t *testing.T) {
	// A=- S=' T=5 R=4 K=( H=£ N=, D=$ I=8 O=9
	if got := Reverse("ASTRAKHAN RADIO"); got != "-'54-(£-, 4-$89" {
		t.Errorf("forward: %q", got)
	}
	for _, s := range []string{"ASTRAKHAN RADIO", "NOAA 1234 WARNING", "WIND 25/30 KT"} {
		if back := Reverse(Reverse(s)); back != s {
			t.Errorf("Reverse twice: %q -> %q", s, back)
		}
	}
	if got := Reverse("-'54-"); got != "ASTRA" {
		t.Errorf("figures to letters: %q", got)
	}
	if got := Reverse("A B~\n"); got != "- ?~\n" {
		t.Errorf("unchanged characters: %q", got)
	}
}
