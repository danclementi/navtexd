package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Deleted records are kept in trash.jsonl, so that the last deletions can be undone. Each deletion is one line holding the
// whole batch; an undo is a second line naming the batch it brought back. Batches older than TrashKeep are dropped when the
// store opens, and so are the oldest ones beyond TrashMaxBytes.
const (
	trashFile     = "trash.jsonl"
	TrashKeep     = 30 * 24 * time.Hour
	TrashMaxBytes = 64 << 20
)

type trashLine struct {
	Batch int64     `json:"b,omitempty"`
	At    time.Time `json:"at,omitempty"`
	Recs  []*Record `json:"recs,omitempty"`
	Undo  int64     `json:"u,omitempty"` // this batch was restored
}

// TrashBatch describes one deletion that can be undone.
type TrashBatch struct {
	ID    int64
	At    time.Time
	Count int
}

func (s *Store) trashPath() string { return filepath.Join(s.dir, trashFile) }

// loadTrash reads the file, drops what is old or too big (rewriting it), and sets s.trash (oldest first). It also moves s.next
// past every ID in it, so a record that is undone gets its own ID back.
func (s *Store) loadTrash() error {
	var lines []trashLine
	undone := map[int64]bool{}
	err := scanLines(s.trashPath(), func(b []byte) error {
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
		return err
	}
	var keep []trashLine
	cut := time.Now().Add(-TrashKeep)
	for _, l := range lines {
		if !undone[l.Batch] && l.At.After(cut) {
			keep = append(keep, l)
		}
	}
	rewrite := len(keep) != len(lines) || len(undone) > 0
	var size int
	enc := make([][]byte, len(keep))
	for i, l := range keep {
		enc[i], _ = json.Marshal(l)
		size += len(enc[i]) + 1
	}
	for size > TrashMaxBytes && len(keep) > 1 {
		size -= len(enc[0]) + 1
		keep, enc, rewrite = keep[1:], enc[1:], true
	}
	if rewrite {
		var out []byte
		for _, e := range enc {
			out = append(out, e...)
			out = append(out, '\n')
		}
		tmp := s.trashPath() + ".tmp"
		if err := os.WriteFile(tmp, out, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, s.trashPath()); err != nil {
			return err
		}
	}
	for _, l := range keep {
		s.trash = append(s.trash, TrashBatch{ID: l.Batch, At: l.At, Count: len(l.Recs)})
		for _, r := range l.Recs {
			if r.ID >= s.next {
				s.next = r.ID + 1
			}
		}
	}
	return nil
}

func (s *Store) appendTrash(l trashLine) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.trashPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}

// archive saves the records that are about to be deleted (store locked).
func (s *Store) archive(ids []int64) error {
	var recs []*Record
	for _, id := range ids {
		if r, ok := s.byID[id]; ok {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		return nil
	}
	b := TrashBatch{ID: time.Now().UnixNano(), At: time.Now().UTC(), Count: len(recs)}
	if err := s.appendTrash(trashLine{Batch: b.ID, At: b.At, Recs: recs}); err != nil {
		return err
	}
	s.trash = append(s.trash, b)
	return nil
}

// LastDeletion is the deletion that Undo would bring back.
func (s *Store) LastDeletion() (TrashBatch, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.trash) == 0 {
		return TrashBatch{}, false
	}
	return s.trash[len(s.trash)-1], true
}

// Undo restores the records of the last deletion and returns them. A record whose ID has been taken since (the file was
// compacted and the number reused) gets a new one.
func (s *Store) Undo() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.trash) == 0 {
		return nil, ErrNotFound
	}
	last := s.trash[len(s.trash)-1]
	var recs []*Record
	err := scanLines(s.trashPath(), func(b []byte) error {
		var l trashLine
		if json.Unmarshal(b, &l) == nil && l.Batch == last.ID {
			recs = l.Recs
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range recs {
		if _, taken := s.byID[r.ID]; taken {
			r.ID = s.next
		}
		if r.ID >= s.next {
			s.next = r.ID + 1
		}
		r.Version++ // clients that kept the old copy refetch it
		if err := s.appendOp(op{Op: "put", R: r}); err != nil {
			return out, err
		}
		s.byID[r.ID] = r
		s.recs = append(s.recs, r)
		out = append(out, *r)
	}
	s.sortRecs()
	if err := s.appendTrash(trashLine{Undo: last.ID}); err != nil {
		return out, err
	}
	s.trash = s.trash[:len(s.trash)-1]
	return out, nil
}
