package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

// Deleted records stay in the database, marked as deleted, for TrashKeep. The last deletions (and the edits made with
// UpdateUndoable) form a stack that Undo takes back one at a time, and Restore brings back any records from the trash, in any
// order.
const TrashKeep = 30 * 24 * time.Hour

// TrashBatch describes one action (a deletion or an edit of a record) that can be undone. The actions form one stack:
// Undo takes back the latest.
type TrashBatch struct {
	ID    int64
	At    time.Time
	Count int
	Kind  string    // "delete" or "edit"
	Desc  string    // for an edit: what was changed
	RecID int64     // the (first) record concerned
	Rx    time.Time // when that record was received
}

// TrashedRecord is a record with what the trash knows about it (DeletedAt and Batch are empty for a record in the log).
type TrashedRecord struct {
	Record
	DeletedAt time.Time `json:"deleted_at"`
	Batch     int64     `json:"batch"`
}

// The latest action that still has something to bring back.
const lastBatchSQL = `SELECT id, at, kind, descr FROM batches b
 WHERE (kind = 'edit' AND EXISTS (SELECT 1 FROM edit_history e WHERE e.batch_id = b.id))
    OR (kind = 'delete' AND EXISTS (SELECT 1 FROM records r WHERE r.batch_id = b.id))
 ORDER BY id DESC LIMIT 1`

// LastDeletion is the action that Undo would take back.
func (s *Store) LastDeletion() (TrashBatch, bool) {
	var b TrashBatch
	var at string
	if err := s.rdb.QueryRow(lastBatchSQL).Scan(&b.ID, &at, &b.Kind, &b.Desc); err != nil {
		return TrashBatch{}, false
	}
	b.At = parseTS(at)
	if b.Kind == "edit" {
		var data string
		rows, err := s.rdb.Query(`SELECT data FROM edit_history WHERE batch_id=? ORDER BY rowid`, b.ID)
		if err != nil {
			return TrashBatch{}, false
		}
		defer rows.Close()
		for rows.Next() {
			b.Count++
			if b.Count == 1 && rows.Scan(&data) == nil {
				if r, err := decode(data); err == nil {
					b.RecID, b.Rx = r.ID, r.Received
				}
			}
		}
		return b, true
	}
	var rx string
	s.rdb.QueryRow(`SELECT count(*) FROM records WHERE batch_id=?`, b.ID).Scan(&b.Count)
	s.rdb.QueryRow(`SELECT id, received FROM records WHERE batch_id=? ORDER BY id LIMIT 1`, b.ID).Scan(&b.RecID, &rx)
	b.Rx = parseTS(rx)
	return b, true
}

// Undo takes back the latest action: the records of the last deletion return to the log (with their old numbers), or the record
// goes back to how it was before the last edit. It returns the records that changed.
func (s *Store) Undo() ([]Record, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var bid int64
	var at, kind, desc string
	if err := tx.QueryRow(lastBatchSQL).Scan(&bid, &at, &kind, &desc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var out []Record
	gained := 0
	if kind == "edit" { // the record goes back to how it was before the edit
		old, err := collect(tx, `SELECT data FROM edit_history WHERE batch_id=? ORDER BY rowid`, bid)
		if err != nil {
			return nil, err
		}
		for _, r := range old {
			var ver int
			var del sql.NullString
			err := tx.QueryRow(`SELECT version, deleted_at FROM records WHERE id=?`, r.ID).Scan(&ver, &del)
			switch {
			case err == nil:
				r.Version = ver + 1 // clients that kept the edited copy refetch it
				if err := updateRec(tx, &r); err != nil {
					return nil, err
				}
				if del.Valid { // (deleted since, and that deletion is no longer on the stack)
					gained++
				}
			case errors.Is(err, sql.ErrNoRows): // (emptied from the trash since)
				r.Version++
				if err := insertRec(tx, &r); err != nil {
					return nil, err
				}
				gained++
			default:
				return nil, err
			}
			out = append(out, r)
		}
		if _, err := tx.Exec(`DELETE FROM edit_history WHERE batch_id=?`, bid); err != nil {
			return nil, err
		}
	} else {
		recs, err := collect(tx, `SELECT b.data FROM records r JOIN bodies b ON b.id = r.id WHERE r.batch_id=? AND r.deleted_at IS NOT NULL ORDER BY r.id`, bid)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			r.Version++ // clients that kept the old copy refetch it
			if err := updateRec(tx, &r); err != nil {
				return nil, err
			}
			out = append(out, r)
			gained++
		}
	}
	if _, err := tx.Exec(`DELETE FROM batches WHERE id=?`, bid); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.live.Add(int64(gained))
	return out, nil
}

// collect reads the records of a query that returns one data column, completely, so that the transaction can write afterwards.
func collect(tx interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, query string, args ...any) ([]Record, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		r, err := decode(data)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Trash lists the deleted records matching f (honouring Limit and Offset; the most recently deleted first) and the total number
// of matches.
func (s *Store) Trash(f Filter) ([]TrashedRecord, int) { return s.queryRows(f, true) }

// Restore brings records back from the trash, whichever deletion they belong to and in any order, and returns them (with new
// versions, so that clients that kept an old copy refetch it). Numbers that are not in the trash are ignored.
func (s *Store) Restore(ids ...int64) ([]Record, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var out []Record
	for i := 0; i < len(ids); i += idChunk {
		part := ids[i:min(i+idChunk, len(ids))]
		args := make([]any, len(part))
		for k, id := range part {
			args[k] = id
		}
		recs, err := collect(tx, `SELECT b.data FROM records r JOIN bodies b ON b.id = r.id WHERE r.deleted_at IS NOT NULL AND r.id IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			r.Version++
			if err := updateRec(tx, &r); err != nil {
				return nil, err
			}
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err := dropEmptyBatches(tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.live.Add(int64(len(out)))
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// dropEmptyBatches removes the deletions that have nothing left in the trash.
func dropEmptyBatches(tx execer) error {
	_, err := tx.Exec(`DELETE FROM batches WHERE kind = 'delete' AND NOT EXISTS (SELECT 1 FROM records r WHERE r.batch_id = batches.id)`)
	return err
}

// maybePurge empties the old trash at most once an hour.
func (s *Store) maybePurge() {
	now := time.Now().Unix()
	if last := s.lastPurge.Load(); now-last < 3600 || !s.lastPurge.CompareAndSwap(last, now) {
		return
	}
	if err := s.purgeTrash(); err != nil {
		s.dbError("emptying the trash", err)
	}
}

// purgeTrash removes what was deleted or edited more than TrashKeep ago.
func (s *Store) purgeTrash() error {
	s.lastPurge.Store(time.Now().Unix())
	cut := ts(time.Now().Add(-TrashKeep))
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM bodies WHERE id IN (SELECT id FROM records WHERE deleted_at IS NOT NULL AND deleted_at < ?)`,
		`DELETE FROM records WHERE deleted_at IS NOT NULL AND deleted_at < ?`,
		`DELETE FROM edit_history WHERE batch_id IN (SELECT id FROM batches WHERE kind = 'edit' AND at < ?)`,
		`DELETE FROM batches WHERE kind = 'edit' AND at < ?`,
	} {
		if _, err := tx.Exec(q, cut); err != nil {
			return err
		}
	}
	if err := dropEmptyBatches(tx); err != nil {
		return err
	}
	return tx.Commit()
}
