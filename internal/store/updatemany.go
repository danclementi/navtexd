package store

import (
	"time"
)

// UpdateManyUndoable stores several edited records as ONE action that Undo takes back (the records as they were are saved
// first; desc says what was done). A record that was edited or deleted meanwhile (its version differs) is skipped. It returns
// the records as stored.
func (s *Store) UpdateManyUndoable(recs []Record, desc string) ([]Record, error) {
	if len(recs) == 0 {
		return nil, nil
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var bid int64
	var out []Record
	for _, r := range recs {
		var ver int
		var old string
		err := tx.QueryRow(`SELECT r.version, b.data FROM records r JOIN bodies b ON b.id = r.id WHERE r.id=? AND r.deleted_at IS NULL`, r.ID).Scan(&ver, &old)
		if err != nil || ver != r.Version { // (gone or changed since: leave it)
			continue
		}
		if bid == 0 {
			res, err := tx.Exec(`INSERT INTO batches(at, kind, descr) VALUES (?, 'edit', ?)`, ts(time.Now()), desc)
			if err != nil {
				return nil, err
			}
			if bid, err = res.LastInsertId(); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`INSERT INTO edit_history(batch_id, rec_id, data) VALUES (?, ?, ?)`, bid, r.ID, old); err != nil {
			return nil, err
		}
		r.Version++
		normalize(&r)
		if err := updateRec(tx, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
