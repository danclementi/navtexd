package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC)

func rec(min int, b1 string, area int, text string) Record {
	return Record{Channel: 518000, Received: t0.Add(time.Duration(min) * time.Minute), Kind: "message", B1: b1, NavArea: area,
		Lines: []Line{{Time: t0, Text: text}}}
}

func TestAddQueryUpdateDelete(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Add(rec(10, "E", 1, "GALE WARNING NITON"))
	b, _ := s.Add(rec(5, "F", 4, "HAZARDOUS SEAS"))
	c, _ := s.Add(rec(20, "E", 4, "ICE REPORT"))
	if a.ID != 1 || b.ID != 2 || c.ID != 3 || a.Version != 1 || a.Station != "UNID" {
		t.Fatalf("ids/version: %+v %+v", a, c)
	}
	all, total := s.Query(Filter{})
	if total != 3 || all[0].ID != b.ID || all[2].ID != c.ID { // ordered by receive time
		t.Errorf("order: %v", []int64{all[0].ID, all[1].ID, all[2].ID})
	}
	if r, n := s.Query(Filter{B1: "E"}); n != 2 || len(r) != 2 {
		t.Errorf("B1 filter: %d", n)
	}
	if _, n := s.Query(Filter{B1: "*", NavArea: 4}); n != 2 {
		t.Errorf("area filter: %d", n)
	}
	if _, n := s.Query(Filter{Text: "gale"}); n != 1 {
		t.Errorf("text filter: %d", n)
	}
	if r, n := s.Query(Filter{From: t0.Add(8 * time.Minute), To: t0.Add(20 * time.Minute)}); n != 1 || r[0].ID != a.ID {
		t.Errorf("time filter: %d", n)
	}
	if r, n := s.Query(Filter{Newest: true, Limit: 1, Offset: 1}); n != 3 || len(r) != 1 || r[0].ID != a.ID {
		t.Errorf("paging: %+v", r)
	}

	a.B1, a.Manual = "G", true
	upd, err := s.Update(a)
	if err != nil || upd.Version != 2 {
		t.Fatalf("update: %v %+v", err, upd)
	}
	if _, err := s.Update(a); err != ErrConflict { // stale version
		t.Errorf("stale update: %v", err)
	}
	if _, err := s.Update(Record{ID: 99}); err != ErrNotFound {
		t.Errorf("missing update: %v", err)
	}
	if n, _ := s.Delete(b.ID, 77); n != 1 {
		t.Errorf("deleted %d", n)
	}
	s.Close()

	// reopen: everything persisted, ids continue
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Count() != 2 {
		t.Fatalf("count after reopen: %d", s.Count())
	}
	if r, _ := s.Get(a.ID); r.B1 != "G" || !r.Manual || r.Version != 2 {
		t.Errorf("reopened record: %+v", r)
	}
	d, _ := s.Add(rec(30, "H", 2, "x"))
	if d.ID != 4 {
		t.Errorf("next id %d", d.ID)
	}
	if n, _ := s.DeleteWhere(Filter{To: t0.Add(15 * time.Minute)}); n != 1 {
		t.Errorf("DeleteWhere removed %d", n)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 2 {
		t.Errorf("count after compact %d", s.Count())
	}
	s.Add(rec(40, "J", 2, "after compact"))
	s.Close()
	s, _ = Open(t.TempDir())
	s.Close()
}

func TestCompactedFileReloads(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for i := 0; i < 5; i++ {
		s.Add(rec(i, "E", 1, "x"))
	}
	s.Delete(2)
	s.Compact()
	s.Add(rec(9, "F", 1, "y"))
	s.Close()
	s, _ = Open(dir)
	defer s.Close()
	if s.Count() != 5 {
		t.Errorf("count %d", s.Count())
	}
	if _, ok := s.Get(2); ok {
		t.Error("deleted record came back")
	}
}

// The message file of earlier versions is converted when the database is first created; a damaged last line is skipped.
func TestDamagedLastLineIgnored(t *testing.T) {
	dir := t.TempDir()
	good := `{"op":"put","r":{"id":1,"v":1,"ch":518,"rx":"2026-09-30T06:00:00Z","end":"2026-09-30T06:01:00Z","kind":"message","b1":"E","b2":"A","ser":"01","area":4,"sid":0,"st":"UNID","lines":[{"t":"2026-09-30T06:00:00Z","q":50.00,"x":"HELLO"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte(good+`{"op":"put","r":{"id":2,"rx":"2026-`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil || s.Count() != 1 {
		t.Fatalf("open: %v count %d", err, s.Count())
	}
	if r, ok := s.Get(1); !ok || r.Text() != "HELLO" {
		t.Errorf("converted record: %+v", r)
	}
	s.Close()
}

func TestSyslogAndQuality(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for i := 0; i < 5; i++ {
		s.Log("info", strings.Repeat("x", i+1))
	}
	if l := s.Syslog(2); len(l) != 2 || l[1].Text != "xxxxx" {
		t.Errorf("syslog tail: %+v", l)
	}
	s.AddQuality(t0, 40)
	s.AddQuality(t0.Add(20*time.Second), 60)
	s.AddQuality(t0.Add(70*time.Second), 10)
	s.Close()
	s, _ = Open(dir)
	defer s.Close()
	q := s.QualitySince(t0)
	if len(q) != 2 || q[0].Quality != 50 || q[1].Quality != 10 || !q[0].Time.Equal(t0) {
		t.Errorf("quality: %+v", q)
	}
	if len(s.Syslog(0)) != 5 {
		t.Errorf("syslog reload: %d", len(s.Syslog(0)))
	}
}
