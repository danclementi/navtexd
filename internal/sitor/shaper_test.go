package sitor

import (
	"strings"
	"testing"
)

func shaped(cfg Config, in string) string {
	d := NewDecoder(cfg)
	for i, r := range in {
		d.shape.put(string(r), int64(i))
	}
	d.shape.flush()
	var b strings.Builder
	for _, e := range d.events {
		b.WriteString(e.Text)
	}
	return b.String()
}

func TestShaper(t *testing.T) {
	cfg := DefaultConfig()
	for _, c := range []struct{ in, want string }{
		{"TEXT.\nBRKZCZC EA78", "TEXT.\nBRK\nZCZC EA78"},
		{"MORE TEXTZCZC FE80", "MORE TEXT\nZCZC FE80"},
		{"ZCZC AB12", "ZCZC AB12"},                   // already at line start
		{"A\nZCZC AB12", "A\nZCZC AB12"},             // no double break
		{"ZCX ZCZ", "ZCX ZCZ"},                       // partial matches pass through unchanged
		{"ABRK TEXT", "ABRK TEXT"},                   // BRK inside a word is not a marker
		{"END BRK\nMORE", "END BRK\n\nMORE"},         // marker followed by a break gets one of its own
		{"ZZCZC X", "Z\nZCZC X"},                     // held Z released when the next Z restarts the match
		{"ZCZ", "ZCZ"},                               // held characters released at the end
		{"NNNN\nBRK ZCZC X", "NNNN\nBRK\n \nZCZC X"}, // the space after BRK starts a line, so ZCZC breaks again
	} {
		if got := shaped(cfg, c.in); got != c.want {
			t.Errorf("%q -> %q, want %q", c.in, got, c.want)
		}
	}
	off := cfg
	off.BreakBeforeZCZC, off.BreakAfterBRK = false, false
	if got := shaped(off, "XBRKZCZC"); got != "XBRKZCZC" {
		t.Errorf("breaks disabled: %q", got)
	}
}

func TestUnreadablePolicy(t *testing.T) {
	// nearest codeword of a word with too few marks; only the DX copy exists
	bad := word{hard: 0b0000011} // two marks: invalid
	for t7 := 0; t7 < 7; t7++ {
		bad.s[t7] = -0.8
	}
	bad.s[0], bad.s[1], bad.s[2], bad.s[6] = 0.9, 0.9, 0.5, 0.4 // soft values point at 0x47 "A" ...
	bad.hard, bad.ok = 0b0000011, false                         // ... but the hard word is invalid (as if it were)
	for _, c := range []struct {
		pol  Unreadable
		want string
	}{{Nearest, "unreadable-nearest"}, {Star, "*"}} {
		cfg := DefaultConfig()
		cfg.Order, cfg.Unreadable, cfg.BreakBeforeZCZC, cfg.BreakAfterBRK = LSB, c.pol, false, false
		d := NewDecoder(cfg)
		d.interps = [2]*interp{{order: LSB}, nil}
		d.decodeSlot(bad, nil, 0)
		var got strings.Builder
		for _, e := range d.events {
			got.WriteString(e.Text)
		}
		if c.pol == Star && got.String() != "*" {
			t.Errorf("star policy printed %q", got.String())
		}
		if c.pol == Nearest && (got.String() == "*" || got.String() == "") {
			t.Errorf("nearest policy printed %q", got.String())
		}
	}
	// a valid copy anywhere makes the character readable
	good := word{hard: 0b1000111, ok: true}
	for t7 := 0; t7 < 7; t7++ {
		good.s[t7] = -1
		if good.hard>>t7&1 == 1 {
			good.s[t7] = 1
		}
	}
	cfg := DefaultConfig()
	cfg.Order, cfg.Unreadable, cfg.BreakBeforeZCZC, cfg.BreakAfterBRK = LSB, Star, false, false
	d := NewDecoder(cfg)
	d.interps = [2]*interp{{order: LSB}, nil}
	d.decodeSlot(bad, &good, 0)
	if len(d.events) != 1 || d.events[0].Text == "*" {
		t.Errorf("readable repeat still starred: %v", d.events)
	}
}
