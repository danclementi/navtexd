package store

import (
	"strings"
	"testing"
	"time"
)

func TestDeleteUndo(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 4; i++ {
		r, err := s.Add(Record{Received: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), Kind: "message", B1: "E"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if _, ok := s.LastDeletion(); ok {
		t.Fatal("nothing deleted yet")
	}
	s.Delete(ids[0])
	s.Delete(ids[1], ids[2])
	if s.Count() != 1 {
		t.Fatalf("count %d", s.Count())
	}
	s.Close()
	s, err = Open(dir) // the undo stack survives a restart
	if err != nil {
		t.Fatal(err)
	}
	b, ok := s.LastDeletion()
	if !ok || b.Count != 2 {
		t.Fatalf("last deletion %+v %v", b, ok)
	}
	rs, err := s.Undo()
	if err != nil || len(rs) != 2 || s.Count() != 3 {
		t.Fatalf("undo: %v %d %d", err, len(rs), s.Count())
	}
	if _, ok := s.Get(ids[1]); !ok {
		t.Error("restored record keeps its ID")
	}
	if b, _ = s.LastDeletion(); b.Count != 1 {
		t.Errorf("then the earlier deletion: %+v", b)
	}
	if _, err := s.Undo(); err != nil || s.Count() != 4 {
		t.Fatalf("second undo: %v %d", err, s.Count())
	}
	if _, err := s.Undo(); err == nil {
		t.Error("nothing left to undo")
	}
	n, _ := s.Add(Record{Received: time.Now(), Kind: "message"})
	if n.ID <= ids[3] {
		t.Errorf("new ID %d reused", n.ID)
	}
	s.Close()
}

func TestExportImport(t *testing.T) {
	a, _ := Open(t.TempDir())
	for i := 0; i < 3; i++ {
		a.Add(Record{Received: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), Kind: "message", Channel: 518, Lines: []Line{{Text: "LINE " + string(rune('A'+i))}}})
	}
	var buf strings.Builder
	if n, err := a.Export(&buf, Filter{}); err != nil || n != 3 {
		t.Fatalf("export %d %v", n, err)
	}
	b, _ := Open(t.TempDir())
	b.Add(Record{Received: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), Kind: "message", Channel: 518, Lines: []Line{{Text: "LINE B"}}}) // same as one of them
	b.Add(Record{Received: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Kind: "message", Channel: 490})
	res, err := b.Import(strings.NewReader(buf.String()), false)
	if err != nil || res.Added != 2 || res.Skipped != 1 || b.Count() != 4 {
		t.Fatalf("merge %+v %v count %d", res, err, b.Count())
	}
	res, err = b.Import(strings.NewReader(buf.String()), true)
	if err != nil || res.Added != 3 || res.Replaced != 4 || res.Backup == "" || b.Count() != 3 {
		t.Fatalf("replace %+v %v count %d", res, err, b.Count())
	}
	if len(b.Backups()) != 1 {
		t.Errorf("backups %v", b.Backups())
	}
	if _, err := b.Import(strings.NewReader("not a log\n"), true); err == nil || b.Count() != 3 {
		t.Errorf("garbage must be refused and change nothing: %v %d", err, b.Count())
	}
}
