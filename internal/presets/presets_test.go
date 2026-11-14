package presets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func good() Filter {
	return Filter{Version: 1, Zone: "local", From: End{D: "today-1", S: "sunset-1h"}, To: End{D: "today", S: "sunrise+1h"}, Freq: "518,490", Kind: "message", Text: "gale"}
}

func TestValidate(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
	utc := Filter{Version: 1, Zone: "utc", From: End{Date: "2026-11-10", Time: "08:00"}, To: End{Date: "2026-11-11"}}
	if err := utc.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Filter){
		"version":           func(f *Filter) { f.Version = 2 },
		"zone":              func(f *Filter) { f.Zone = "mars" },
		"relative in utc":   func(f *Filter) { f.Zone = "utc" },
		"bad relative":      func(f *Filter) { f.From.D = "today-9" },
		"fixed as relative": func(f *Filter) { f.From.D = "2026-11-10" },
		"bad sun":           func(f *Filter) { f.To.S = "noon" },
		"time as sun":       func(f *Filter) { f.To.S = "08:00" },
		"sun as time":       func(f *Filter) { f.To.Time = "sunset" },
		"bad date":          func(f *Filter) { f.From.Date = "10-11-2026" },
		"pick characters":   func(f *Filter) { f.Freq = "518;DROP" },
		"long text":         func(f *Filter) { f.Text = strings.Repeat("x", 201) },
		"distance":          func(f *Filter) { f.KMin = "far" },
	} {
		f := good()
		mod(&f)
		if f.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStorePersistsAndSurvivesDamage(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if n := countUsed(s); n != 0 {
		t.Fatalf("new store has %d", n)
	}
	g := good()
	if err := s.Set(3, &g); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(11, &g); err == nil {
		t.Error("slot 11 accepted")
	}
	if err := s.Set(10, &g); err != nil {
		t.Errorf("slot 10: %v", err)
	}
	_ = s.Set(10, nil)
	bad := good()
	bad.Zone = "x"
	if err := s.Set(1, &bad); err == nil {
		t.Error("an invalid preset was stored")
	}
	again := Open(dir)
	if f := again.All()[2]; f == nil || f.From.D != "today-1" || f.From.S != "sunset-1h" || f.Text != "gale" {
		t.Errorf("not kept: %+v", f)
	}
	if err := again.Set(3, nil); err != nil || Open(dir).All()[2] != nil {
		t.Errorf("clear: %v", err)
	}
	// a damaged file is set aside, the store starts empty and works
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := Open(dir)
	if countUsed(s2) != 0 {
		t.Error("damaged file not ignored")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".bad")); err != nil {
		t.Errorf("no .bad copy: %v", err)
	}
	if err := s2.Set(1, &g); err != nil {
		t.Error(err)
	}
}

func countUsed(s *Store) int {
	n := 0
	for _, f := range s.All() {
		if f != nil {
			n++
		}
	}
	return n
}

func TestNamesAreUnique(t *testing.T) {
	s := Open(t.TempDir())
	a, b := good(), good()
	a.Name, b.Name = "Night Europe", "night europe"
	if err := s.Set(1, &a); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(2, &b); err == nil || !strings.Contains(err.Error(), "preset 1") {
		t.Errorf("a name used twice (any case): %v", err)
	}
	if err := s.Set(1, &a); err != nil { // the same slot may be stored again with its own name
		t.Errorf("restoring a slot's own name: %v", err)
	}
	b.Name = "Preset 7"
	if err := s.Set(2, &b); err == nil {
		t.Error(`"Preset 7" in slot 2 accepted`)
	}
	b.Name = "Preset 2"
	if err := s.Set(2, &b); err != nil {
		t.Errorf(`"Preset 2" in slot 2: %v`, err)
	}
	b.Name = "Daily"
	if err := s.Set(2, &b); err != nil {
		t.Error(err)
	}
	// an 8-slot file from the first version still loads
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, FileName), []byte(`{"slots":[null,{"version":1,"zone":"utc","from":{},"to":{},"freq":"518","rcv":"","area":"","b1":"","kind":"","text":"","kmin":"","kmax":"","unid":false,"omit":false},null,null,null,null,null,null]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := Open(d).All()[1]; f == nil || f.Freq != "518" || f.Name != "" {
		t.Errorf("old file: %+v", f)
	}
}
