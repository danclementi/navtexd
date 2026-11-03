package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Earlier versions kept the log in messages.jsonl (one JSON line for each change of a record) and the trash in trash.jsonl.
// migrateLegacy converts them the first time the database is opened: the live records and the trash (deletions that could still
// be undone, and the saved copies from edits) go into the database in one transaction, and the two files are renamed to
// *.migrated (never deleted). It returns a line for the system log, or "" when there was nothing to convert.
func (s *Store) migrateLegacy() (string, error) {
	jsonl := filepath.Join(s.dir, msgFile)
	trashPath := filepath.Join(s.dir, "trash.jsonl")
	if _, err := os.Stat(jsonl); err != nil {
		return "", nil // (a trash file without a message file is of no use)
	}
	// only into a database that never held a record
	var ever int
	if err := s.wdb.QueryRow(`SELECT count(*) FROM sqlite_sequence WHERE name='records'`).Scan(&ever); err != nil {
		return "", err
	}
	var n int
	if err := s.wdb.QueryRow(`SELECT count(*) FROM records`).Scan(&n); err != nil {
		return "", err
	}
	if ever > 0 || n > 0 {
		return "", nil
	}

	// First pass: which line holds the last version of each record (the file may be far larger than the memory).
	type head struct {
		Op string `json:"op"`
		R  *struct {
			ID int64 `json:"id"`
		} `json:"r,omitempty"`
		ID int64 `json:"id,omitempty"`
	}
	last := map[int64]int{} // record number -> line of its last "put"
	var maxID int64
	lineNo := 0
	err := scanLines(jsonl, func(b []byte) error {
		lineNo++
		var o head
		if json.Unmarshal(b, &o) != nil {
			return nil // a damaged (e.g. half-written last) line is skipped
		}
		switch o.Op {
		case "put":
			if o.R != nil {
				last[o.R.ID] = lineNo
				maxID = max(maxID, o.R.ID)
			}
		case "del":
			delete(last, o.ID)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	type trashLine struct {
		Batch int64     `json:"b,omitempty"`
		At    time.Time `json:"at,omitempty"`
		Kind  string    `json:"k,omitempty"`
		Desc  string    `json:"d,omitempty"`
		Recs  []*Record `json:"recs,omitempty"`
		Undo  int64     `json:"u,omitempty"`
	}
	var lines []trashLine
	undone := map[int64]bool{}
	err = scanLines(trashPath, func(b []byte) error {
		var l trashLine
		if json.Unmarshal(b, &l) != nil {
			return nil
		}
		if l.Undo != 0 {
			undone[l.Undo] = true
		} else if l.Batch != 0 {
			lines = append(lines, l)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	cut := time.Now().Add(-TrashKeep)
	var keep []trashLine
	for _, l := range lines {
		if !undone[l.Batch] && l.At.After(cut) {
			keep = append(keep, l)
			for _, r := range l.Recs {
				maxID = max(maxID, r.ID)
			}
		}
	}

	tx, err := s.wdb.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	prep := func(r *Record) {
		if r.Station == "" {
			r.Station = "UNID"
		}
	}
	// Second pass: the records go into the database in file order.
	taken := make(map[int64]bool, len(last))
	lineNo = 0
	err = scanLines(jsonl, func(b []byte) error {
		lineNo++
		var o struct {
			Op string  `json:"op"`
			R  *Record `json:"r,omitempty"`
		}
		var h head
		if json.Unmarshal(b, &h) != nil || h.Op != "put" || h.R == nil || last[h.R.ID] != lineNo {
			return nil
		}
		if json.Unmarshal(b, &o) != nil || o.R == nil {
			return nil
		}
		prep(o.R)
		taken[o.R.ID] = true
		if err := insertRec(tx, o.R); err != nil {
			return fmt.Errorf("record %d: %w", o.R.ID, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	trashed, edits := 0, 0
	for _, l := range keep {
		kind := "delete"
		if l.Kind == "edit" {
			kind = "edit"
		}
		res, err := tx.Exec(`INSERT INTO batches(at, kind, descr) VALUES (?, ?, ?)`, ts(l.At), kind, l.Desc)
		if err != nil {
			return "", err
		}
		bid, _ := res.LastInsertId()
		for _, r := range l.Recs {
			prep(r)
			if kind == "edit" {
				data, err := json.Marshal(r)
				if err != nil {
					return "", err
				}
				if _, err := tx.Exec(`INSERT INTO edit_history(batch_id, rec_id, data) VALUES (?, ?, ?)`, bid, r.ID, string(data)); err != nil {
					return "", err
				}
				edits++
				continue
			}
			if taken[r.ID] { // the number was used again after the deletion: the record gets a new one
				maxID++
				r.ID = maxID
			}
			taken[r.ID] = true
			if err := insertRec(tx, r); err != nil {
				return "", err
			}
			if _, err := tx.Exec(`UPDATE records SET deleted_at=?, batch_id=? WHERE id=?`, ts(l.At), bid, r.ID); err != nil {
				return "", err
			}
			trashed++
		}
	}
	// the numbers of everything that ever existed stay used
	if _, err := tx.Exec(`UPDATE sqlite_sequence SET seq = ? WHERE name='records' AND seq < ?`, maxID, maxID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO sqlite_sequence(name, seq) SELECT 'records', ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='records')`, maxID); err != nil {
		return "", err
	}
	var got int
	if err := tx.QueryRow(`SELECT count(*) FROM records WHERE deleted_at IS NULL`).Scan(&got); err != nil {
		return "", err
	}
	if got != len(last) {
		return "", fmt.Errorf("check failed: %d records read, %d stored", len(last), got)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}

	for _, p := range []string{jsonl, trashPath} {
		if _, err := os.Stat(p); err == nil {
			if err := os.Rename(p, p+".migrated"); err != nil {
				// (the data is safely in the database; a leftover file is only reported)
				return "", fmt.Errorf("converted, but %s could not be renamed: %w", filepath.Base(p), err)
			}
		}
	}
	return fmt.Sprintf("message log converted to %s: %d messages, %d deleted ones in the trash, %d saved edits; the earlier files are kept as *.migrated",
		dbFile, len(last), trashed, edits), nil
}
