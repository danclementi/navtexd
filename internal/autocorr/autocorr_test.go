package autocorr

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"navtex/internal/sitor"
	"navtex/internal/vocab"
)

func small() *Corrector {
	return NewWith(vocab.Vocabulary{"WATERS": 50, "WINDS": 90, "WATERY": 5, "VISIBILITY": 20, "SECTOR": 3, "SEVEN": 1, "SEVER": 1, "SEVEL": 1, "SEVEY": 1, "SEVEX": 1})
}

func TestCorrectLine(t *testing.T) {
	c, cfg := small(), DefaultConfig()
	for _, tc := range []struct{ in, want string }{
		{"WATE~S 5 TO 10 KT", "[WATERS] 5 TO 10 KT"},
		{"W~NDS AND WATE*S", "[WINDS] AND [WATERS]"},
		{"WATE~~ NEAR", "WATE~~ NEAR"},     // more than half? no: 2 of 5 is 40%, but WATERS/WATERY fit
		{"~~~~S", "~~~~S"},                 // too many unreadable
		{"W~NDS", "[WINDS]"},               // exactly one match
		{"SEVE~ NM", "SEVE~ NM"},           // five candidates are too many
		{"AB~D WINDS", "AB~D WINDS"},       // too short
		{"0900~ WATE~S", "0900~ [WATERS]"}, // a mark touching a digit is not a word
		{"VISIBILIT~ 5", "[VISIBILITY] 5"},
		{"NOTHING WRONG", "NOTHING WRONG"},
	} {
		got, _ := c.CorrectLine(tc.in, cfg)
		if tc.in == "WATE~~ NEAR" {
			if got != "[WATERS|WATERY] NEAR" {
				t.Errorf("%q -> %q", tc.in, got)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
	if got, _ := c.CorrectLine("WATE~S", Config{}); got != "WATE~S" {
		t.Errorf("disabled: %q", got)
	}
}

func TestFixPositionsAndBody(t *testing.T) {
	c, cfg := small(), DefaultConfig()
	line, fx := c.CorrectLine("A W~NDS B", cfg)
	if len(fx) != 1 || line[fx[0].Start:fx[0].End] != "[WINDS]" || fx[0].Orig != "W~NDS" {
		t.Errorf("%q %+v", line, fx)
	}
	body, notes := c.Body([]string{"ZCZC AA01", "W~NDS", "CLEAN"}, cfg)
	if body != "ZCZC AA01\n[WINDS]\nCLEAN" || !reflect.DeepEqual(notes, []string{"W~NDS corrected to WINDS"}) {
		t.Errorf("%q %v", body, notes)
	}
	if b, _ := c.Body([]string{"ALL CLEAN"}, cfg); b != "" {
		t.Errorf("nothing changed must give an empty body, got %q", b)
	}
}

func TestReversals(t *testing.T) {
	c, cfg := small(), DefaultConfig()
	figs := sitor.Reverse("WATERS") // what "WATERS" looks like in the wrong shift
	if strings.ContainsAny(figs, "WATERS") {
		t.Fatalf("test setup: %q", figs)
	}
	got := c.Reversals("HEAVY "+figs+" "+sitor.Reverse("WINDS")+" END", cfg)
	if len(got) != 1 || got[0].Text != "WATERS WINDS" {
		t.Errorf("%+v", got)
	}
	if got := c.Reversals("5 TO 10 KT 12345", cfg); len(got) != 0 {
		t.Errorf("plain figures must not be suggested: %+v", got)
	}
}

func TestBuiltinAndExtra(t *testing.T) {
	c := New()
	if c.Size() < 2000 {
		t.Fatalf("built-in vocabulary has %d words", c.Size())
	}
	if got, _ := c.CorrectLine("W~NDS", DefaultConfig()); got == "W~NDS" {
		t.Error("WINDS should be found in the built-in vocabulary")
	}
	p := filepath.Join(t.TempDir(), "words.txt")
	os.WriteFile(p, []byte("# mine\nQUUXWORD\n"), 0o644)
	if ch, err := c.Refresh(p); !ch || err != nil {
		t.Fatalf("refresh: %v %v", ch, err)
	}
	if got, _ := c.CorrectLine("QUUX~ORD", DefaultConfig()); got != "[QUUXWORD]" {
		t.Errorf("extra word: %q", got)
	}
	if ch, _ := c.Refresh(p); ch {
		t.Error("unchanged file must not reload")
	}
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(p, []byte("OTHERWORD\n"), 0o644)
	os.Chtimes(p, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	c.mu.Lock()
	c.checked = time.Time{}
	c.mu.Unlock()
	if ch, _ := c.Refresh(p); !ch {
		t.Error("changed file must reload")
	}
	if got, _ := c.CorrectLine("QUUX~ORD", DefaultConfig()); got != "QUUX~ORD" {
		t.Errorf("old extra word must be gone: %q", got)
	}
}

// Words in the figure shift can contain non-ASCII characters (the pound sign); the reverser check must not index by byte.
func TestReversalsWithNonASCII(t *testing.T) {
	c := New()
	cfg := Config{Enabled: true, MinLen: 5, MaxLen: 16, MaxMarkedPct: 50, MaxCandidates: 4}
	for _, line := range []string{"-'54-(£- 4-$89", "£££££££££ ££££££", "ab£dé~ ~~~~~~~~", "\u00a3\u00a3\u00a3\u00a3\u00a3\u00a3\u00a3"} {
		_ = c.Reversals(line, cfg) // must not panic
	}
}
