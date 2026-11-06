package store

import (
	"testing"
	"time"
)

func TestImportRecords(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := time.Date(2026, 1, 18, 8, 52, 10, 0, time.UTC)
	mk := func(i int, text string) *Record {
		at := t0.Add(time.Duration(i) * time.Hour)
		return &Record{Channel: 490000, Kind: "message", Received: at, End: at, Lines: []Line{{Time: at, Text: text}}}
	}
	// ids that are not usable (all 0) are numbered in the order given
	res, err := st.ImportRecords([]*Record{mk(0, "A"), mk(1, "B")}, false)
	if err != nil || res.Added != 2 || res.Skipped != 0 {
		t.Fatalf("first import: %+v %v", res, err)
	}
	// the same records again: nothing is added; one new
	res, err = st.ImportRecords([]*Record{mk(0, "A"), mk(1, "B"), mk(2, "C")}, false)
	if err != nil || res.Added != 1 || res.Skipped != 2 {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if n := st.Count(); n != 3 {
		t.Fatalf("%d records", n)
	}
	// replace: the log becomes the records; the old one is saved
	a, b := mk(5, "X"), mk(6, "Y")
	a.ID, b.ID = 41, 42
	res, err = st.ImportRecords([]*Record{a, b}, true)
	if err != nil || res.Added != 2 || res.Replaced != 3 || res.Backup == "" {
		t.Fatalf("replace: %+v %v", res, err)
	}
	if got, ok := st.Get(41); !ok || got.Lines[0].Text != "X" {
		t.Fatalf("record 41 not kept: %+v %v", got, ok)
	}
	if _, err := st.ImportRecords(nil, false); err == nil {
		t.Fatal("nothing to import was accepted")
	}
}

func TestImportReplaceNeverReusesNumbers(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)
	mk := func(text string) *Record {
		return &Record{Channel: 490000, Kind: "message", Received: at, End: at, Lines: []Line{{Time: at, Text: text}}}
	}
	if _, err := st.ImportRecords([]*Record{mk("A"), mk("B")}, false); err != nil {
		t.Fatal(err)
	}
	a, b := mk("X"), mk("Y")
	a.ID, b.ID = 1, 2 // numbers the log used before, now for other records
	if _, err := st.ImportRecords([]*Record{a, b}, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if _, ok := st.Get(id); ok {
			t.Errorf("number %d came back for another record", id)
		}
	}
	if got, ok := st.Get(3); !ok || got.Lines[0].Text != "X" || got.Version != 1 {
		t.Errorf("record 3: %+v %v", got, ok)
	}
}
