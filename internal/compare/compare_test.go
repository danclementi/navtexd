package compare

import (
	"strings"
	"testing"
	"time"
)

func TestAlign(t *testing.T) {
	cases := []struct {
		a, b  string
		kinds string
	}{
		{"SAME TEXT", "same  text\n", ""},
		{"GALE WARNING 5", "GALE WARNING 6", "d"},
		{"GALE WARNING", "GALE WA~NING", "u"},
		{"GALE WARNING", "GALE WA*NING", "u"},
		{"GALE WARNING AT SEA", "GALE WARNING SEA", "aaa"},
		{"WIND NW", "WIND FROM NW", "bbbbb"},
	}
	for _, c := range cases {
		ops, ok := Align(Normalize(c.a), Normalize(c.b))
		var k strings.Builder
		for _, o := range ops {
			k.WriteByte(o.Kind)
		}
		if !ok || k.String() != c.kinds {
			t.Errorf("%q vs %q: kinds %q ok %v, want %q", c.a, c.b, k.String(), ok, c.kinds)
		}
	}
}

func TestAlignTooDifferent(t *testing.T) {
	if _, ok := Align(Normalize(strings.Repeat("A", 500)), Normalize("B")); ok {
		t.Error("expected failure")
	}
	if _, ok := Align(Normalize(strings.Repeat("A", 800)), Normalize(strings.Repeat("B", 800))); ok {
		t.Error("expected failure for heavily different text")
	}
}

func TestPairing(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c := New(Config{NameA: "ch1", NameB: "ch3", Pair: time.Minute, Wait: 30 * time.Second})
	mk := func(key string, dt time.Duration, text string) Msg {
		return Msg{Key: key, Start: t0.Add(dt), End: t0.Add(dt + 20*time.Second), Text: text}
	}
	// identical, timing differs by 3 s: nothing
	if r := c.Add(0, mk("EA78", 0, "GALE WARNING"), t0); r != nil {
		t.Fatal(r)
	}
	if r := c.Add(1, mk("EA78", 3*time.Second, "GALE  WARNING\n"), t0.Add(2*time.Second)); r != nil {
		t.Fatal(r)
	}
	// differing, the other order
	c.Add(1, mk("FE80", time.Hour, "VISIBILITY 5 MILES"), t0)
	r := c.Add(0, mk("FE80", time.Hour+2*time.Second, "VISIBILITY 6 MILES"), t0)
	if len(r) != 1 || r[0].Diffs != 1 || !strings.Contains(r[0].Summary, "FE80") {
		t.Fatalf("%+v", r)
	}
	// same key hours apart is not the same message
	c.Add(0, mk("EA78", 5*time.Hour, "X"), t0)
	if r := c.Add(1, mk("EA78", 9*time.Hour, "X"), t0); r != nil {
		t.Fatal(r)
	}
	// missing copies are reported after the wait, only if the other side runs
	if r := c.Expire(t0.Add(10*time.Second), [2]bool{true, true}); len(r) != 0 {
		t.Fatal(r)
	}
	r = c.Expire(t0.Add(time.Minute), [2]bool{true, true})
	if len(r) != 2 || r[0].Missing == "" {
		t.Fatalf("%+v", r)
	}
	c.Add(0, mk("GA11", 0, "Y"), t0)
	if r := c.Expire(t0.Add(time.Minute), [2]bool{true, false}); len(r) != 0 {
		t.Fatal(r)
	}
}

func TestMinDiffs(t *testing.T) {
	t0 := time.Now()
	c := New(Config{NameA: "a", NameB: "b", Pair: time.Minute, Wait: time.Minute, MinDiffs: 2})
	c.Add(0, Msg{Key: "K", Start: t0, Text: "ABC"}, t0)
	if r := c.Add(1, Msg{Key: "K", Start: t0, Text: "ABD"}, t0); r != nil {
		t.Error("one difference should be below the minimum")
	}
}
