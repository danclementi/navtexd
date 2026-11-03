package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- system log ---

func (s *Store) loadSyslog() error {
	path := filepath.Join(s.dir, logFile)
	if err := scanLines(path, func(b []byte) error {
		var e SyslogEntry
		if json.Unmarshal(b, &e) == nil {
			s.sys = append(s.sys, e)
		}
		return nil
	}); err != nil {
		return err
	}
	s.logLines = len(s.sys)
	if len(s.sys) > SyslogKeep {
		s.sys = append([]SyslogEntry(nil), s.sys[len(s.sys)-SyslogKeep:]...)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	s.logF = f
	return err
}

// Log appends a system log line (level: info, warn, error).
func (s *Store) Log(level, text string) SyslogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := SyslogEntry{Time: time.Now().UTC().Round(time.Millisecond), Level: level, Text: text}
	b, _ := json.Marshal(e)
	s.logF.Write(append(b, '\n'))
	s.logLines++
	s.sys = append(s.sys, e)
	if len(s.sys) > SyslogKeep {
		s.sys = s.sys[len(s.sys)-SyslogKeep:]
	}
	if s.logLines > 2*SyslogKeep { // rewrite the file with the newest lines only
		path := filepath.Join(s.dir, logFile)
		var sb strings.Builder
		for _, x := range s.sys {
			b, _ := json.Marshal(x)
			sb.Write(b)
			sb.WriteByte('\n')
		}
		s.logF.Close()
		if os.WriteFile(path+".tmp", []byte(sb.String()), 0o644) == nil && os.Rename(path+".tmp", path) == nil {
			s.logLines = len(s.sys)
		}
		s.logF, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	}
	return e
}

// Syslog returns the newest n entries (all if n <= 0), oldest first.
func (s *Store) Syslog(n int) []SyslogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.sys
	if n > 0 && len(src) > n {
		src = src[len(src)-n:]
	}
	return append([]SyslogEntry(nil), src...)
}

// --- quality history ---

func (s *Store) loadQuality() error {
	path := filepath.Join(s.dir, qualFile)
	if err := scanLines(path, func(b []byte) error {
		u, q, ok := strings.Cut(string(b), ",")
		if !ok {
			return nil
		}
		ts, e1 := strconv.ParseInt(u, 10, 64)
		v, e2 := strconv.ParseFloat(q, 64)
		if e1 == nil && e2 == nil {
			s.qual = append(s.qual, QPoint{time.Unix(ts, 0).UTC(), v})
		}
		return nil
	}); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	s.qualF = f
	return err
}

// AddQuality records a quality value; values are averaged per minute and written when the minute changes.
func (s *Store) AddQuality(t time.Time, v float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := t.Unix() / 60
	if s.qMinute >= 0 && m != s.qMinute && s.qN > 0 {
		s.flushQualityLocked()
	}
	s.qMinute = m
	s.qSum += v
	s.qN++
}

func (s *Store) flushQualityLocked() {
	if s.qN == 0 {
		return
	}
	p := QPoint{time.Unix(s.qMinute*60, 0).UTC(), s.qSum / float64(s.qN)}
	s.qual = append(s.qual, p)
	fmt.Fprintf(s.qualF, "%d,%.1f\n", p.Time.Unix(), p.Quality)
	s.qSum, s.qN = 0, 0
}

// QualitySince returns the per-minute quality points at or after t.
func (s *Store) QualitySince(t time.Time) []QPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := sort.Search(len(s.qual), func(i int) bool { return !s.qual[i].Time.Before(t) })
	return append([]QPoint(nil), s.qual[i:]...)
}

func scanLines(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}
