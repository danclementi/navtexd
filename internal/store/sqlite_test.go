package store

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func ids(rs []Record) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func same(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The SQL conditions select exactly what Filter.match selects.
func TestSQLFilterAgreesWithMatch(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rnd := rand.New(rand.NewSource(7))
	words := []string{"GALE", "ICE", "50%_OFF", "WARNING", "back\\slash", "NIL", "gale"}
	kinds := []string{"message", "incomplete", "fragment"}
	var all []*Record
	for i := 0; i < 300; i++ {
		r := Record{
			Channel: []int{490000, 518000}[rnd.Intn(2)], Rcv: rnd.Intn(2), Kind: kinds[rnd.Intn(3)],
			Received: t0.Add(time.Duration(rnd.Intn(500)) * time.Minute).Add(time.Duration(rnd.Intn(1000)) * time.Millisecond),
			B1:       string(rune('A' + rnd.Intn(6))), NavArea: rnd.Intn(5), StationID: rnd.Intn(4), DistanceKm: float64(rnd.Intn(900)),
			Lines: []Line{{Text: words[rnd.Intn(len(words))] + " " + words[rnd.Intn(len(words))]}},
		}
		if rnd.Intn(5) == 0 {
			r.Corrected = words[rnd.Intn(len(words))]
		}
		a, err := s.Add(r)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, &a)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Received.Equal(all[j].Received) {
			return all[i].Received.Before(all[j].Received)
		}
		return all[i].ID < all[j].ID
	})
	for n := 0; n < 400; n++ {
		var f Filter
		if rnd.Intn(2) == 0 {
			f.Channel = []int{490000, 518000}[rnd.Intn(2)]
		}
		if rnd.Intn(3) == 0 {
			f.Rcv = 1 + rnd.Intn(2)
		}
		if rnd.Intn(3) == 0 {
			f.NavArea = rnd.Intn(5)
		}
		if rnd.Intn(3) == 0 {
			f.B1 = []string{"*", "A", "C", "F"}[rnd.Intn(4)]
		}
		if rnd.Intn(4) == 0 {
			f.NavAreas = map[int]bool{rnd.Intn(5): true, rnd.Intn(5): true}
		}
		if rnd.Intn(4) == 0 {
			f.B1s = map[string]bool{"B": true, "D": rnd.Intn(2) == 0}
		}
		if rnd.Intn(8) == 0 {
			f.NavAreas = map[int]bool{}
		}
		f.OmitUnid = rnd.Intn(4) == 0
		f.Unidentified = rnd.Intn(6) == 0
		if rnd.Intn(3) == 0 {
			f.From = t0.Add(time.Duration(rnd.Intn(500)) * time.Minute).Add(time.Duration(rnd.Intn(2000)) * time.Microsecond)
		}
		if rnd.Intn(3) == 0 {
			f.To = t0.Add(time.Duration(rnd.Intn(500)) * time.Minute).Add(time.Duration(rnd.Intn(2000)) * time.Microsecond)
		}
		if rnd.Intn(4) == 0 {
			f.Kind = kinds[rnd.Intn(3)]
		}
		if rnd.Intn(5) == 0 {
			f.MaxDistance = float64(rnd.Intn(900))
		}
		if rnd.Intn(5) == 0 {
			f.MinDistance = float64(rnd.Intn(900))
		}
		if rnd.Intn(3) == 0 {
			f.Text = []string{"gale", "ICE", "50%", "_OFF", "k\\s", "%", "zzz", "WARNING NIL"}[rnd.Intn(8)]
		}
		f.Newest = rnd.Intn(2) == 0
		var want []int64
		for _, r := range all {
			if f.match(r) {
				want = append(want, r.ID)
			}
		}
		if f.Newest {
			for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
				want[i], want[j] = want[j], want[i]
			}
		}
		got, total := s.Query(f)
		if total != len(want) || !same(ids(got), want) {
			t.Fatalf("filter %+v:\n got  %d %v\n want %d %v", f, total, ids(got), len(want), want)
		}
		if len(want) > 3 {
			k := rnd.Intn(len(want))
			if pos := s.Locate(f, want[k]); pos != k {
				t.Fatalf("filter %+v: Locate(%d) = %d, want %d", f, want[k], pos, k)
			}
			f.Offset, f.Limit = 1, 2
			got, total = s.Query(f)
			if total != len(want) || !same(ids(got), want[1:3]) {
				t.Fatalf("paging %+v: %v want %v", f, ids(got), want[1:3])
			}
		}
	}
}

// Any records can be brought back from the trash, in any order, and the undo stack stays right.
func TestRestoreAnyOrder(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var id []int64
	for i := 0; i < 6; i++ {
		r, _ := s.Add(Record{Received: t0.Add(time.Duration(i) * time.Minute), Kind: "message", B1: "E", Lines: []Line{{Text: fmt.Sprintf("MSG %d", i)}}})
		id = append(id, r.ID)
	}
	s.Delete(id[0], id[1])
	s.Delete(id[2])
	s.Delete(id[3], id[4])
	if s.Count() != 1 {
		t.Fatalf("count %d", s.Count())
	}
	tr, total := s.Trash(Filter{})
	if total != 5 || len(tr) != 5 || tr[0].DeletedAt.IsZero() || tr[0].Batch == 0 {
		t.Fatalf("trash: %d %+v", total, tr)
	}
	if _, n := s.Trash(Filter{Text: "msg 2"}); n != 1 {
		t.Errorf("search in the trash: %d", n)
	}
	// restore one record from the middle of the first batch, and one from the last batch
	rs, err := s.Restore(id[1], id[4], 9999)
	if err != nil || len(rs) != 2 || rs[0].ID != id[1] || rs[1].ID != id[4] || rs[0].Version != 2 {
		t.Fatalf("restore: %+v %v", rs, err)
	}
	if s.Count() != 3 {
		t.Fatalf("count after restore %d", s.Count())
	}
	if _, ok := s.Get(id[1]); !ok {
		t.Error("restored record is back")
	}
	// the stack: batch {3} (4 was restored), then {2}, then {0} (1 was restored)
	b, ok := s.LastDeletion()
	if !ok || b.Count != 1 || b.RecID != id[3] {
		t.Fatalf("last %+v", b)
	}
	if rs, err := s.Undo(); err != nil || len(rs) != 1 || rs[0].ID != id[3] {
		t.Fatalf("undo: %+v %v", rs, err)
	}
	if rs, _ := s.Restore(id[2]); len(rs) != 1 { // restoring the record of the next batch empties that batch
		t.Fatal("restore of the middle batch")
	}
	b, ok = s.LastDeletion()
	if !ok || b.Count != 1 || b.RecID != id[0] {
		t.Fatalf("then the first batch: %+v", b)
	}
	s.Close()

	s, _ = Open(dir) // all of it survives a restart
	defer s.Close()
	if b, ok = s.LastDeletion(); !ok || b.RecID != id[0] || s.Count() != 5 {
		t.Fatalf("after reopening: %+v %d", b, s.Count())
	}
	if _, err := s.Undo(); err != nil || s.Count() != 6 {
		t.Fatal("last undo")
	}
	if _, ok := s.LastDeletion(); ok {
		t.Fatal("nothing left")
	}
	if _, n := s.Trash(Filter{}); n != 0 {
		t.Fatalf("trash should be empty: %d", n)
	}
}

// What is older than TrashKeep is removed for good, and its number is never used again.
func TestTrashPurgeKeepsNumbers(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	a, _ := s.Add(Record{Received: t0, Kind: "message"})
	b, _ := s.Add(Record{Received: t0.Add(time.Minute), Kind: "message"})
	s.Delete(b.ID)
	s.UpdateUndoable(Record{ID: a.ID, Version: a.Version, Received: t0, Kind: "message", B1: "X"}, "edit")
	old := ts(time.Now().Add(-TrashKeep - time.Hour))
	s.wdb.Exec(`UPDATE records SET deleted_at=? WHERE id=?`, old, b.ID)
	s.wdb.Exec(`UPDATE batches SET at=?`, old)
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if _, n := s.Trash(Filter{}); n != 0 {
		t.Errorf("old trash remains: %d", n)
	}
	if _, ok := s.LastDeletion(); ok {
		t.Error("old actions remain on the undo stack")
	}
	var edits int
	s.wdb.QueryRow(`SELECT count(*) FROM edit_history`).Scan(&edits)
	if edits != 0 {
		t.Errorf("edit copies remain: %d", edits)
	}
	s.Close()
	s, _ = Open(dir)
	defer s.Close()
	c, _ := s.Add(Record{Received: t0.Add(time.Hour), Kind: "message"})
	if c.ID <= b.ID {
		t.Errorf("number %d was used again (b was %d)", c.ID, b.ID)
	}
}

// Everything the earlier versions wrote is converted: the log, the trash, the saved edits.
func TestMigrateFromJSONL(t *testing.T) {
	dir := t.TempDir()
	rec := func(id int64, min int, text string) string {
		return fmt.Sprintf(`{"id":%d,"v":1,"ch":518,"rx":%q,"end":%q,"kind":"message","b1":"E","b2":"A","ser":"01","area":4,"sid":0,"st":"UNID","lines":[{"t":%q,"q":10.00,"x":%q}]}`,
			id, t0.Add(time.Duration(min)*time.Minute).Format(time.RFC3339), t0.Format(time.RFC3339), t0.Format(time.RFC3339), text)
	}
	var m strings.Builder
	for i := int64(1); i <= 5; i++ {
		fmt.Fprintf(&m, `{"op":"put","r":%s}`+"\n", rec(i, int(i), fmt.Sprintf("TEXT %d", i)))
	}
	m.WriteString(`{"op":"del","id":2}` + "\n")
	m.WriteString(`{"op":"del","id":4}` + "\n")
	m.WriteString(`{"op":"put","r":` + rec(6, 6, "SIX") + `}` + "\n")
	m.WriteString(`{"op":"del","id":6}` + "\n") // (6 was the highest number: it must not come back)
	m.WriteString(`{"op":"put","r":{"id":7,"rx":"2026-`)
	os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte(m.String()), 0o644)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	tr := fmt.Sprintf(`{"b":101,"at":%q,"recs":[%s]}`, old, rec(1, 1, "TOO OLD")) + "\n" +
		fmt.Sprintf(`{"b":102,"at":%q,"recs":[%s,%s]}`, now, rec(2, 2, "TEXT 2"), rec(4, 4, "TEXT 4")) + "\n" +
		fmt.Sprintf(`{"b":103,"at":%q,"recs":[%s]}`, now, rec(6, 6, "SIX")) + "\n" +
		fmt.Sprintf(`{"b":104,"at":%q,"k":"edit","d":"B1 change","recs":[%s]}`, now, rec(3, 3, "TEXT 3 BEFORE")) + "\n" +
		fmt.Sprintf(`{"b":105,"at":%q,"recs":[%s]}`, now, rec(5, 5, "UNDONE")) + "\n" + `{"u":105}` + "\n"
	os.WriteFile(filepath.Join(dir, "trash.jsonl"), []byte(tr), 0o644)

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Count() != 3 { // 1, 3, 5
		t.Fatalf("count %d", s.Count())
	}
	for _, f := range []string{"messages.jsonl", "trash.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s should have been renamed", f)
		}
		if _, err := os.Stat(filepath.Join(dir, f+".migrated")); err != nil {
			t.Errorf("%s.migrated: %v", f, err)
		}
	}
	if l := s.Syslog(1); len(l) != 1 || !strings.Contains(l[0].Text, "converted") {
		t.Errorf("system log: %+v", l)
	}
	if _, n := s.Trash(Filter{}); n != 3 { // 2, 4, 6
		t.Errorf("trash %d", n)
	}
	last, ok := s.LastDeletion()
	if !ok || last.Kind != "edit" || last.Desc != "B1 change" || last.RecID != 3 {
		t.Fatalf("last action %+v", last)
	}
	if _, err := s.Undo(); err != nil { // the edit
		t.Fatal(err)
	}
	if r, _ := s.Get(3); r.Text() != "TEXT 3 BEFORE" {
		t.Errorf("edit undone: %q", r.Text())
	}
	if rs, err := s.Undo(); err != nil || len(rs) != 1 || rs[0].ID != 6 {
		t.Fatalf("undo of the deletion of 6: %v %v", rs, err)
	}
	if rs, err := s.Undo(); err != nil || len(rs) != 2 {
		t.Fatalf("undo of the deletion of 2 and 4: %v %v", rs, err)
	}
	if _, err := s.Undo(); err == nil {
		t.Error("the old and the undone deletions must not be on the stack")
	}
	n, _ := s.Add(Record{Received: t0, Kind: "message"})
	if n.ID != 7 {
		t.Errorf("next number %d, want 7", n.ID)
	}
	s.Close()
	s, _ = Open(dir) // a second start converts nothing
	defer s.Close()
	if s.Count() != 7 {
		t.Errorf("count after reopening: %d", s.Count())
	}
}

// A converted database that is empty must not be converted again from a file that is left over.
func TestNoSecondConversion(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	a, _ := s.Add(Record{Received: t0, Kind: "message"})
	s.Delete(a.ID)
	s.Close()
	os.WriteFile(filepath.Join(dir, "messages.jsonl"), []byte(`{"op":"put","r":{"id":1,"rx":"2026-09-30T06:00:00Z","kind":"message","lines":[]}}`+"\n"), 0o644)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Count() != 0 {
		t.Errorf("count %d: a leftover message file must be ignored", s.Count())
	}
}

// People who open the file with another program see readable tables and views.
func TestViews(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.Add(Record{Received: t0, Channel: 518000, Kind: "message", B1: "E", B2: "A", Serial: "12", NavArea: 4, Station: "Boston",
		Lines: []Line{{Time: t0, Quality: 71.5, Text: "FIRST"}, {Time: t0.Add(time.Second), Quality: 60, Text: "SECOND"}}})
	d, _ := s.Add(Record{Received: t0.Add(time.Minute), Kind: "message", Lines: []Line{{Text: "GONE"}}})
	s.Delete(d.ID)
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rx, st, text string
	var ch int
	if err := db.QueryRow(`SELECT received_utc, channel_khz, station, text FROM messages`).Scan(&rx, &ch, &st, &text); err != nil {
		t.Fatal(err)
	}
	if rx != "2026-09-30T06:00:00.000Z" || ch != 518 || st != "Boston" || text != "FIRST\nSECOND" {
		t.Errorf("messages view: %q %d %q %q", rx, ch, st, text)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
	if n != 1 {
		t.Errorf("messages view shows %d rows", n)
	}
	rows, err := db.Query(`SELECT line_no, quality, text FROM message_lines ORDER BY line_no`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var no int
		var q float64
		var x string
		rows.Scan(&no, &q, &x)
		got = append(got, fmt.Sprintf("%d %.1f %s", no, q, x))
	}
	rows.Close()
	if strings.Join(got, "|") != "1 71.5 FIRST|2 60.0 SECOND" {
		t.Errorf("message_lines view: %v", got)
	}
	db.QueryRow(`SELECT count(*) FROM trash_messages`).Scan(&n)
	if n != 1 {
		t.Errorf("trash view shows %d rows", n)
	}
}

// A backup is a complete database that opens as a store.
func TestBackupIsDatabase(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	defer s.Close()
	for i := 0; i < 3; i++ {
		s.Add(Record{Received: t0.Add(time.Duration(i) * time.Minute), Kind: "message"})
	}
	name, err := s.Backup("test", 2)
	if err != nil || !strings.HasSuffix(name, ".db") {
		t.Fatalf("backup: %q %v", name, err)
	}
	s.Add(Record{Received: t0.Add(time.Hour), Kind: "message"})
	dir2 := t.TempDir()
	data, err := os.ReadFile(filepath.Join(dir, "backups", name))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir2, "messages.db"), data, 0o644)
	b, err := Open(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Count() != 3 {
		t.Errorf("the copy has %d records, want the 3 of that moment", b.Count())
	}
	if len(s.Backups()) != 1 {
		t.Errorf("backups: %v", s.Backups())
	}
}

// Queries, backups, compaction and writes may all happen at once.
func TestConcurrentUse(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 200; i++ {
		s.Add(Record{Received: t0.Add(time.Duration(i) * time.Second), Kind: "message", Lines: []Line{{Text: "LINE"}}})
	}
	var wg sync.WaitGroup
	errc := make(chan error, 16)
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := f(); err != nil {
					errc <- err
					return
				}
			}
		}()
	}
	run(func() error { _, n := s.Query(Filter{Text: "line", Limit: 10}); _ = n; return nil })
	run(func() error { _, err := s.Backup("c", 2); return err })
	run(func() error { return s.Compact() })
	run(func() error {
		r, err := s.Add(Record{Received: time.Now(), Kind: "message"})
		if err == nil {
			_, err = s.Delete(r.ID)
		}
		return err
	})
	run(func() error { var b strings.Builder; _, err := s.Export(&b, Filter{}); return err })
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if s.Count() != 200 {
		t.Errorf("count %d", s.Count())
	}
}

func TestDamagedDatabaseRefused(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for i := 0; i < 50; i++ {
		s.Add(Record{Received: t0.Add(time.Duration(i) * time.Second), Kind: "message", Lines: []Line{{Text: strings.Repeat("X", 500)}}})
	}
	s.Close()
	os.Remove(filepath.Join(dir, "messages.db-wal"))
	f, err := os.OpenFile(filepath.Join(dir, "messages.db"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte("this is not a database page, it is garbage written over the file"), 4096*3)
	f.WriteAt([]byte("garbage garbage garbage garbage garbage garbage garbage garbage"), 4096*5+100)
	f.Close()
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Error("a damaged database must be refused")
	} else if !strings.Contains(err.Error(), "backups") && !strings.Contains(err.Error(), "database") && !strings.Contains(err.Error(), "malformed") {
		t.Logf("error: %v", err)
	}
}

// A database of the first version holds the carrier frequency in kHz; opening it converts it to Hz.
func TestUpgradeFrequencyToHz(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	a, _ := s.Add(Record{Received: t0, Channel: 518000, Kind: "message", Lines: []Line{{Text: "ONE"}}})
	b, _ := s.Add(Record{Received: t0.Add(time.Minute), Channel: 4209500, Kind: "message", Lines: []Line{{Text: "TWO"}}})
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(dir, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE records SET channel = channel / 1000 WHERE id = ` + fmt.Sprint(a.ID),
		`UPDATE bodies SET data = json_set(data, '$.ch', 518) WHERE id = ` + fmt.Sprint(a.ID),
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if r, ok := s.Get(a.ID); !ok || r.Channel != 518000 {
		t.Errorf("record 1: %+v", r)
	}
	if r, ok := s.Get(b.ID); !ok || r.Channel != 4209500 {
		t.Errorf("record 2: %+v", r)
	}
	if got, n := s.Query(Filter{Channel: 518000}); n != 1 || len(got) != 1 || got[0].ID != a.ID {
		t.Errorf("filter by 518000: %d", n)
	}
	if FormatKHz(4209500) != "4209.5" || FormatKHz(518000) != "518" {
		t.Errorf("FormatKHz %s %s", FormatKHz(4209500), FormatKHz(518000))
	}
}
