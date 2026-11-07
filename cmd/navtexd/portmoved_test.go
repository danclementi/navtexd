package main

import "testing"

func TestPortMoved(t *testing.T) {
	for _, c := range []struct {
		wanted, actual string
		want           bool
	}{
		{":8080", "[::]:8080", false},
		{"127.0.0.1:8080", "127.0.0.1:8080", false},
		{"127.0.0.1:8080", "127.0.0.1:8081", true},
		{":8080", "[::]:8083", true},
		{":8080", "garbage", true},
	} {
		if got := portMoved(c.wanted, c.actual); got != c.want {
			t.Errorf("portMoved(%q, %q) = %v, want %v", c.wanted, c.actual, got, c.want)
		}
	}
}
