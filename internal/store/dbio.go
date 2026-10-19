package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const backupDir = "backups"

// Backup is a saved copy of the message file.
type Backup struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Time time.Time `json:"time"`
}

// Size is the size of the message file in bytes.
func (s *Store) Size() int64 {
	if fi, err := os.Stat(filepath.Join(s.dir, msgFile)); err == nil {
		return fi.Size()
	}
	return 0
}

// Backup copies the message file to backups/<what>-<time>.jsonl and keeps only the newest keep copies made for that reason
// (keep <= 0: all). It returns the name of the copy.
func (s *Store) Backup(what string, keep int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backupLocked(what, keep)
}

func (s *Store) backupLocked(what string, keep int) (string, error) {
	dir := filepath.Join(s.dir, backupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := os.Open(filepath.Join(s.dir, msgFile))
	if err != nil {
		return "", err
	}
	defer src.Close()
	name := fmt.Sprintf("messages-%s-%s.jsonl", what, time.Now().UTC().Format("20060102T150405Z"))
	dst, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	if keep > 0 {
		all, _ := filepath.Glob(filepath.Join(dir, "messages-"+what+"-*.jsonl"))
		sort.Strings(all) // the time is in the name
		for len(all) > keep {
			os.Remove(all[0])
			all = all[1:]
		}
	}
	return name, nil
}

// Backups lists the saved copies, newest first.
func (s *Store) Backups() []Backup {
	all, _ := filepath.Glob(filepath.Join(s.dir, backupDir, "messages-*.jsonl"))
	out := []Backup{}
	for _, p := range all {
		if fi, err := os.Stat(p); err == nil {
			out = append(out, Backup{Name: filepath.Base(p), Size: fi.Size(), Time: fi.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// Export writes the records matching f (ignoring Limit and Offset) to w in the format of the message file, oldest first.
func (s *Store) Export(w io.Writer, f Filter) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bw := bufio.NewWriter(w)
	n := 0
	for _, r := range s.recs {
		if !f.match(r) {
			continue
		}
		b, err := json.Marshal(op{Op: "put", R: r})
		if err != nil {
			return n, err
		}
		bw.Write(b)
		bw.WriteByte('\n')
		n++
	}
	return n, bw.Flush()
}

// recKey says whether two records are the same transmission (for merging).
func recKey(r *Record) string {
	return fmt.Sprintf("%d|%d|%s|%s", r.Received.UnixMilli(), r.Channel, r.Kind, r.Text())
}

// ImportResult says what an import did.
type ImportResult struct {
	Read     int    `json:"read"`     // records in the file
	Added    int    `json:"added"`    // records now in the log that were not before
	Skipped  int    `json:"skipped"`  // merge: records that were already there
	Replaced int    `json:"replaced"` // replace: records that were in the log before
	Backup   string `json:"backup"`   // the copy of the old log
}

// Import reads a message file (or an export) from r. With replace the log becomes exactly the file's records (the old log is
// saved to the backups folder first); otherwise records that are not already in the log (same time, channel, kind and text) are
// added with new numbers. A file without any record is refused and nothing changes.
func (s *Store) Import(r io.Reader, replace bool) (ImportResult, error) {
	in := map[int64]*Record{}
	var order []int64
	bad := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var o op
		if json.Unmarshal([]byte(line), &o) != nil || (o.Op != "put" && o.Op != "del") {
			bad++
			continue
		}
		switch {
		case o.Op == "put" && o.R != nil && o.R.ID > 0 && !o.R.Received.IsZero():
			if _, seen := in[o.R.ID]; !seen {
				order = append(order, o.R.ID)
			}
			in[o.R.ID] = o.R
		case o.Op == "del":
			delete(in, o.ID)
		default:
			bad++
		}
	}
	if err := sc.Err(); err != nil {
		return ImportResult{}, err
	}
	recs := make([]*Record, 0, len(in))
	for _, id := range order {
		if r, ok := in[id]; ok {
			recs = append(recs, r)
		}
	}
	if len(recs) == 0 {
		return ImportResult{}, errors.New("the file has no log records (is it a messages.jsonl or an export from this program?)")
	}
	if bad > len(recs) {
		return ImportResult{}, fmt.Errorf("the file does not look like a log: %d unreadable lines", bad)
	}
	for _, r := range recs {
		normalize(r)
		if r.Station == "" {
			r.Station = "UNID"
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := ImportResult{Read: len(recs)}
	if replace {
		name, err := s.backupLocked("before-import", 0)
		if err != nil {
			return res, fmt.Errorf("could not save the current log first: %w", err)
		}
		res.Backup, res.Replaced = name, len(s.recs)
		s.recs, s.byID = nil, map[int64]*Record{}
		for _, r := range recs {
			s.byID[r.ID] = r
			s.recs = append(s.recs, r)
			if r.ID >= s.next {
				s.next = r.ID + 1
			}
		}
		res.Added = len(recs)
		s.sortRecs()
		return res, s.rewriteLocked()
	}
	have := map[string]bool{}
	for _, r := range s.recs {
		have[recKey(r)] = true
	}
	for _, r := range recs {
		k := recKey(r)
		if have[k] {
			res.Skipped++
			continue
		}
		have[k] = true
		r.ID, r.Version = s.next, 1
		if err := s.appendOp(op{Op: "put", R: r}); err != nil {
			return res, err
		}
		s.next++
		s.byID[r.ID] = r
		s.recs = append(s.recs, r)
		res.Added++
	}
	s.sortRecs()
	return res, nil
}
