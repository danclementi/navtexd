package store

import (
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
