package app

import (
	"testing"

	"navtex/internal/segment"
	"navtex/internal/store"
)

func TestWordCount(t *testing.T) {
	for _, c := range []struct {
		text string
		min  int
		want int
	}{
		{"", 4, 0},
		{"GALE WARNING FOR SEA AREA", 4, 3},
		{"GALE WARNING FOR SEA AREA", 3, 5},
		{"WATE~S OF THE NORTH", 5, 1},         // the unreadable character splits WATE~S
		{"[WATERS|WATER] OF THE NORTH", 5, 2}, // a repaired word counts once, as its first candidate
		{"[WATERS|WA] OF THE", 5, 1},
		{"12345 678 ABCD", 4, 1},
		{"abcd efgh", 4, 2},
		{"X*Q~ ZZ QQ", 2, 2},
	} {
		if got := wordCount(c.text, c.min); got != c.want {
			t.Errorf("wordCount(%q, %d) = %d, want %d", c.text, c.min, got, c.want)
		}
	}
}

func TestFragmentKept(t *testing.T) {
	frag := store.Record{Kind: "fragment", Lines: []store.Line{{Text: "XQ WAT~S ZZ KJHG"}}, Corrected: "XQ [WATERS] ZZ KJHG"}
	cfg := segment.Config{MinFragmentWords: 2, FragWordLetters: 4, FragWordsCorrect: false}
	if keep, n := fragmentKept(frag, cfg); keep || n != 1 {
		t.Errorf("raw text: keep=%v n=%d, want dropped with 1 word", keep, n)
	}
	cfg.FragWordsCorrect = true
	if keep, n := fragmentKept(frag, cfg); !keep || n != 2 {
		t.Errorf("after autocorrect: keep=%v n=%d, want kept with 2 words", keep, n)
	}
	cfg.MinFragmentWords = 0
	frag.Lines[0].Text, frag.Corrected = "~~ ** ~", ""
	if keep, _ := fragmentKept(frag, cfg); !keep {
		t.Error("test off must keep everything")
	}
	cfg.MinFragmentWords = 1
	if keep, _ := fragmentKept(frag, cfg); keep {
		t.Error("noise without a word should be dropped")
	}
	msg := store.Record{Kind: "message", Lines: []store.Line{{Text: "~~"}}}
	if keep, _ := fragmentKept(msg, cfg); !keep {
		t.Error("messages are never tested")
	}
}
