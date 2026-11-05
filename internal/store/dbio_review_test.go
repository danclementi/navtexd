package store

// Review tests (not part of the project). See the review report.

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Finding D1: ImportRecords(replace) makes the backup (VACUUM INTO, on its own connection) and only then opens the write
// transaction that deletes everything. A record the decoder stores in between is in neither the backup nor the new log.
// The test keeps adding records while a replace runs and checks that each one that Add reported as stored is still somewhere.
func TestReview_ReplaceLosesConcurrentAdds(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	big := make([]byte, 4000)
	for i := range big {
		big[i] = 'A' + byte(i%26)
	}
	for i := 0; i < 3000; i++ { // a log big enough for the backup to take a moment
		if _, err := s.Add(Record{Channel: 518000, Kind: "message", Received: t0.Add(time.Duration(i) * time.Second), Lines: []Line{{Time: t0, Text: string(big)}}}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var added []int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r, err := s.Add(Record{Channel: 518000, Kind: "fragment", Received: t0.Add(time.Hour * 24), Lines: []Line{{Time: t0, Text: "LIVE"}}})
			if err == nil {
				mu.Lock()
				added = append(added, r.ID)
				mu.Unlock()
			}
			time.Sleep(time.Millisecond)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	res, err := s.ImportRecords([]*Record{{ID: 1, Channel: 518000, Kind: "message", Received: t0, Lines: []Line{{Time: t0, Text: "IMPORTED"}}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()

	inLog := map[int64]bool{}
	recs, _ := s.Query(Filter{})
	for _, r := range recs {
		inLog[r.ID] = true
	}
	bk, err := sql.Open("sqlite", filepath.Join(dir, backupDir, res.Backup))
	if err != nil {
		t.Fatal(err)
	}
	defer bk.Close()
	inBackup := map[int64]bool{}
	rows, err := bk.Query(`SELECT id FROM records`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		inBackup[id] = true
	}
	rows.Close()
	lost := 0
	for _, id := range added {
		if !inLog[id] && !inBackup[id] {
			lost++
		}
	}
	t.Logf("%d records stored during the replace, %d in neither the new log nor the backup", len(added), lost)
	if lost > 0 {
		t.Fatalf("%d records that Add reported as stored were lost by the replace", lost)
	}
}
