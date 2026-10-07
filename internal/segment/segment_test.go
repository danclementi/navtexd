package segment

import (
	"strings"
	"testing"
)

func run(t *testing.T, cfg Config, chunks ...string) []Record {
	t.Helper()
	var out []Record
	s := New(cfg, func(a, b float64) float64 { return 50 }, func(r Record) { out = append(out, r) })
	tm := 0.0
	for _, c := range chunks {
		tm += 1
		s.Feed(c, tm)
	}
	s.Flush(tm)
	return out
}

func TestMessagesAndFragments(t *testing.T) {
	cfg := DefaultConfig()
	recs := run(t, cfg,
		"garbage before the first message here\n",
		"\nZCZC EA78\n", "1. FIRST LINE\n", "2. SECOND ", "LINE NNNN\n",
		"tail text after nnnn that is long enough\n",
		"\nZCZC FE80\n", "cut short message\n", "\nZCZC FA97\n", "ok\n", "NNNN")
	var kinds []string
	for _, r := range recs {
		kinds = append(kinds, r.Kind.String())
	}
	if got := strings.Join(kinds, ","); got != "fragment,message,fragment,incomplete,message" {
		t.Fatalf("kinds = %s", got)
	}
	m := recs[1]
	if m.B1 != "E" || m.B2 != "A" || m.Serial != "78" {
		t.Errorf("header %q %q %q", m.B1, m.B2, m.Serial)
	}
	if m.Body() != "1. FIRST LINE\n2. SECOND LINE" {
		t.Errorf("body %q", m.Body())
	}
	if m.Lines[0].Text != "ZCZC EA78" || m.Lines[len(m.Lines)-1].Text != "NNNN" {
		t.Errorf("lines %v", m.Lines)
	}
	if recs[3].B1 != "F" || recs[3].Body() != "cut short message" {
		t.Errorf("incomplete: %+v", recs[3])
	}
	if recs[4].Kind != Message || recs[4].Body() != "ok" || recs[4].Quality != 50 {
		t.Errorf("last: %+v", recs[4])
	}
}

func TestShortFragmentsDropped(t *testing.T) {
	cfg := DefaultConfig()
	if r := run(t, cfg, "B~K\n"); len(r) != 0 {
		t.Errorf("short fragment kept: %v", r)
	}
	cfg.MinFragmentChars = 0
	if r := run(t, cfg, "B~K\n"); len(r) != 1 {
		t.Errorf("fragment lost with limit 0")
	}
}

func TestIdleClose(t *testing.T) {
	var out []Record
	s := New(DefaultConfig(), nil, func(r Record) { out = append(out, r) })
	s.Feed("ZCZC EA01\nhello\n", 10)
	s.Tick(50)
	if len(out) != 0 {
		t.Fatal("closed too early")
	}
	s.Tick(80)
	if len(out) != 1 || out[0].Kind != Incomplete {
		t.Fatalf("idle close: %+v", out)
	}
}

func TestLongLineBreaks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinFragmentChars = 0
	r := run(t, cfg, strings.Repeat("A", 450))
	if len(r) != 1 || len(r[0].Lines) != 3 || len(r[0].Lines[0].Text) != 200 {
		t.Errorf("lines: %d", len(r[0].Lines))
	}
}
