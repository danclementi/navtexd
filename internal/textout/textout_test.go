package textout

import (
	"strings"
	"testing"
	"time"
)

func TestLineWriter(t *testing.T) {
	var b strings.Builder
	w := LineWriter{W: &b}
	t0 := time.Date(2026, 1, 26, 10, 38, 59, 0, time.UTC)
	w.Write("ZCZC F", t0)
	w.Write("A97\r\nGA", t0.Add(2*time.Second))
	w.Write("LE\r\n\r\nNNNN", t0.Add(5*time.Second))
	w.Finish()
	want := "2026-01-26 10:38:59 > ZCZC FA97\n" +
		"2026-01-26 10:39:01 > GALE\n" +
		"2026-01-26 10:39:04 > \n" +
		"2026-01-26 10:39:04 > NNNN\n"
	if b.String() != want {
		t.Fatalf("got:\n%q\nwant:\n%q", b.String(), want)
	}
}
