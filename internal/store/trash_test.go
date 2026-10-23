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

// A restored record returns to its old place in the list, and Locate finds it in either order.
func TestUndoRestoresPlace(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 5; i++ {
		r, _ := s.Add(Record{Received: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC), Kind: "message", B1: "E"})
		ids = append(ids, r.ID)
	}
	s.Delete(ids[2])
	if s.Locate(Filter{}, ids[2]) != -1 {
		t.Fatal("deleted record still located")
	}
	rs, err := s.Undo()
	if err != nil || len(rs) != 1 || rs[0].ID != ids[2] {
		t.Fatalf("undo: %v %v", rs, err)
	}
	if i := s.Locate(Filter{}, ids[2]); i != 2 {
		t.Fatalf("oldest first: index %d, want 2", i)
	}
	if i := s.Locate(Filter{Newest: true}, ids[0]); i != 4 {
		t.Fatalf("newest first: index %d, want 4", i)
	}
	if i := s.Locate(Filter{B1: "X"}, ids[2]); i != -1 {
		t.Fatalf("filtered out: index %d", i)
	}
}

// Edits go on the same undo stack as deletions: Undo takes back the latest of either.
func TestUndoEdit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Add(Record{Received: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Kind: "message", B1: "E", NavArea: 4})
	b, _ := s.Add(Record{Received: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), Kind: "message", B1: "E"})
	a.B1 = "F"
	a, err = s.UpdateUndoable(a, "B1 change")
	if err != nil || a.B1 != "F" {
		t.Fatalf("edit: %+v %v", a, err)
	}
	s.Delete(b.ID)
	if last, _ := s.LastDeletion(); last.Kind != "delete" || last.RecID != b.ID {
		t.Fatalf("last action %+v", last)
	}
	if _, err := s.Undo(); err != nil { // the deletion first
		t.Fatal(err)
	}
	last, ok := s.LastDeletion()
	if !ok || last.Kind != "edit" || last.Desc != "B1 change" || last.RecID != a.ID || !last.Rx.Equal(a.Received) {
		t.Fatalf("last action %+v %v", last, ok)
	}
	if _, err := s.UpdateUndoable(Record{ID: a.ID, Version: 0}, "x"); err != ErrConflict {
		t.Fatalf("a stale edit must conflict and leave no undo entry: %v", err)
	}
	rs, err := s.Undo()
	if err != nil || len(rs) != 1 || rs[0].B1 != "E" || rs[0].NavArea != 4 {
		t.Fatalf("undo edit: %+v %v", rs, err)
	}
	got, _ := s.Get(a.ID)
	if got.B1 != "E" || got.Version <= a.Version {
		t.Fatalf("record after undo %+v (edited version %d)", got, a.Version)
	}
	if _, ok := s.LastDeletion(); ok {
		t.Fatal("stack should be empty")
	}
	s.Close()
	s, err = Open(dir) // survives a restart
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(a.ID); got.B1 != "E" {
		t.Fatalf("after reopening: %+v", got)
	}
}
