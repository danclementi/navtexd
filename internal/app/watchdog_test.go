package app

import (
	"strings"
	"testing"
	"time"
)

func TestInputWatch(t *testing.T) {
	var logs []string
	w := newInputWatch(10*time.Second, func(l, s string) { logs = append(logs, l+": "+s) })
	base := time.Unix(0, w.lastNew.Load())
	w.check(base.Add(9 * time.Second))
	if len(logs) != 0 {
		t.Fatalf("early warning: %v", logs)
	}
	// blocks keep arriving, but constant: "not changed"
	w.lastAny.Store(base.Add(11 * time.Second).UnixNano())
	w.check(base.Add(11 * time.Second))
	w.check(base.Add(12 * time.Second)) // reported once only
	if len(logs) != 1 || !strings.Contains(logs[0], "have not changed") {
		t.Fatalf("logs %v", logs)
	}
	w.lastNew.Store(base.Add(13 * time.Second).UnixNano())
	w.check(base.Add(14 * time.Second))
	if len(logs) != 2 || !strings.Contains(logs[1], "arriving again") {
		t.Fatalf("logs %v", logs)
	}
	// nothing at all
	w.check(base.Add(30 * time.Second))
	if len(logs) != 3 || !strings.Contains(logs[2], "no new samples") {
		t.Fatalf("logs %v", logs)
	}
	if constant([]complex64{1, 1, 2}) || !constant([]complex64{3, 3, 3}) {
		t.Fatal("constant()")
	}
}
