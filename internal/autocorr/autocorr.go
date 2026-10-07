// Package autocorr repairs words that contain unreadable characters, and finds stretches of text that were decoded in
// the wrong shift. Both use the maritime vocabulary (see cmd/navtexvocab). Nothing here changes the raw text: the
// results are stored or shown separately.
//
// Autocorrection works as in YaND: a word of 5 to 16 characters in which some characters are unreadable ('~' or '*',
// at most half of them) is matched against the vocabulary; the match is shown in [square brackets], several
// possibilities as [A|B|C]. A word with too many possibilities is left alone.
package autocorr

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"navtex/internal/sitor"
	"navtex/internal/vocab"
)

//go:embed maritime_en.txt
var builtin string

// Config selects what is corrected.
type Config struct {
	Enabled       bool
	MinLen        int // shortest and longest word that is looked up
	MaxLen        int
	MaxMarkedPct  int // at most this percentage of a word's characters may be unreadable
	MaxCandidates int // a word that matches more vocabulary words than this is left alone
}

// DefaultConfig is YaND's rule: 5 to 16 characters, at most half unreadable.
func DefaultConfig() Config {
	return Config{Enabled: true, MinLen: 5, MaxLen: 16, MaxMarkedPct: 50, MaxCandidates: 4}
}

type entry struct {
	word  string
	count int
}

// Corrector holds the vocabulary: the built-in list plus an optional user file with more words.
type Corrector struct {
	mu      sync.RWMutex
	byLen   map[int][]entry
	words   map[string]int
	extra   string
	extraAt time.Time // modification time of the extra file when it was loaded
	checked time.Time
}

// New returns a corrector with the built-in vocabulary.
func New() *Corrector {
	v, _ := vocab.Read(strings.NewReader(builtin))
	c := &Corrector{}
	c.set(v)
	return c
}

// NewWith returns a corrector with exactly the given vocabulary (for tests and tools).
func NewWith(v vocab.Vocabulary) *Corrector {
	c := &Corrector{}
	c.set(v)
	return c
}

func (c *Corrector) set(v vocab.Vocabulary) {
	by := map[int][]entry{}
	for w, n := range v {
		by[len(w)] = append(by[len(w)], entry{w, n})
	}
	for _, l := range by {
		sort.Slice(l, func(i, j int) bool {
			if l[i].count != l[j].count {
				return l[i].count > l[j].count
			}
			return l[i].word < l[j].word
		})
	}
	c.mu.Lock()
	c.byLen, c.words = by, v
	c.mu.Unlock()
}

// Size is the number of words known.
func (c *Corrector) Size() int { c.mu.RLock(); defer c.mu.RUnlock(); return len(c.words) }

// Refresh makes the corrector use the built-in vocabulary plus the words in the file at path (empty = none). The file
// is read again when it changes; the modification time is looked at most every five seconds. It returns whether the
// vocabulary changed.
func (c *Corrector) Refresh(path string) (changed bool, err error) {
	c.mu.RLock()
	same := path == c.extra && time.Since(c.checked) < 5*time.Second
	c.mu.RUnlock()
	if same {
		return false, nil
	}
	var mod time.Time
	if path != "" {
		if st, e := os.Stat(path); e == nil {
			mod = st.ModTime()
		}
	}
	c.mu.Lock()
	c.checked = time.Now()
	if path == c.extra && mod.Equal(c.extraAt) {
		c.mu.Unlock()
		return false, nil
	}
	c.extra, c.extraAt = path, mod
	c.mu.Unlock()
	v, _ := vocab.Read(strings.NewReader(builtin))
	if path != "" && !mod.IsZero() {
		extra, e := vocab.Load(path)
		if e != nil {
			err = e
		} else {
			for w, n := range extra {
				if len(w) >= 3 {
					v[w] += n + 1
				}
			}
		}
	}
	c.set(v)
	return true, err
}

// Fix is one corrected word.
type Fix struct {
	Orig  string   `json:"o"` // as received, e.g. "WATE~S"
	Cands []string `json:"c"` // the vocabulary words that fit, most frequent first
	Start int      `json:"s"` // position of the word in the corrected line (the bracket)
	End   int      `json:"e"`
}

func isMark(b byte) bool { return b == '~' || b == '*' }

// match reports whether word w fits pattern p (same length; marks in p match any letter).
func match(p string, w string) bool {
	for i := 0; i < len(p); i++ {
		if !isMark(p[i]) && p[i] != w[i] {
			return false
		}
	}
	return true
}

// CorrectLine corrects the words of one line. It returns the new line and the fixes (positions refer to the new line).
func (c *Corrector) CorrectLine(line string, cfg Config) (string, []Fix) {
	if !cfg.Enabled || !strings.ContainsAny(line, "~*") {
		return line, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out strings.Builder
	var fixes []Fix
	for i := 0; i < len(line); {
		ch := line[i]
		if !(ch >= 'A' && ch <= 'Z' || isMark(ch)) {
			out.WriteByte(ch)
			i++
			continue
		}
		j := i
		marks := 0
		for j < len(line) && (line[j] >= 'A' && line[j] <= 'Z' || isMark(line[j])) {
			if isMark(line[j]) {
				marks++
			}
			j++
		}
		tok := line[i:j]
		touching := i > 0 && line[i-1] >= '0' && line[i-1] <= '9' || j < len(line) && line[j] >= '0' && line[j] <= '9'
		n := len(tok)
		if marks > 0 && !touching && n >= cfg.MinLen && n <= cfg.MaxLen && marks*100 <= cfg.MaxMarkedPct*n && marks < n {
			var cands []string
			for _, e := range c.byLen[n] {
				if match(tok, e.word) {
					if cands = append(cands, e.word); len(cands) > cfg.MaxCandidates {
						break
					}
				}
			}
			if len(cands) > 0 && len(cands) <= cfg.MaxCandidates {
				rep := "[" + strings.Join(cands, "|") + "]"
				fixes = append(fixes, Fix{Orig: tok, Cands: cands, Start: out.Len(), End: out.Len() + len(rep)})
				out.WriteString(rep)
				i = j
				continue
			}
		}
		out.WriteString(tok)
		i = j
	}
	return out.String(), fixes
}

// Body corrects all lines of a message. It returns the corrected lines joined with newlines, or "" when nothing
// changed, and a text for each fix ("WATE~S corrected to WATERS").
func (c *Corrector) Body(lines []string, cfg Config) (string, []string) {
	out := make([]string, len(lines))
	var notes []string
	changed := false
	for i, l := range lines {
		cl, fx := c.CorrectLine(l, cfg)
		out[i] = cl
		for _, f := range fx {
			changed = true
			notes = append(notes, fmt.Sprintf("%s corrected to %s", f.Orig, strings.Join(f.Cands, "|")))
		}
	}
	if !changed {
		return "", nil
	}
	return strings.Join(out, "\n"), notes
}

// Reversal is a stretch of a line that probably was decoded in the wrong shift: Text is what it becomes when its
// letters and figures are swapped.
type Reversal struct {
	Start int    `json:"s"`
	End   int    `json:"e"`
	Text  string `json:"t"`
}

// Reversals suggests which parts of a line to put through the shift reverser: a run of consecutive words without any
// letter whose reversal is a vocabulary word (unreadable characters in it count as wildcards).
func (c *Corrector) Reversals(line string, cfg Config) []Reversal {
	var out []Reversal
	c.mu.RLock()
	defer c.mu.RUnlock()
	start, end := -1, -1
	flush := func() {
		if start >= 0 {
			out = append(out, Reversal{start, end, sitor.Reverse(line[start:end])})
		}
		start = -1
	}
	for i := 0; i < len(line); {
		if line[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' {
			j++
		}
		tok := line[i:j]
		if c.reversible(tok, cfg) {
			if start < 0 {
				start = i
			}
			end = j
		} else {
			flush()
		}
		i = j
	}
	flush()
	return out
}

// reversible: a word with no capital letter (other than marks) that, reversed, reads as a vocabulary word.
func (c *Corrector) reversible(tok string, cfg Config) bool {
	rs := []rune(tok) // the figure shift has non-ASCII characters (e.g. the pound sign): count characters, not bytes
	n := len(rs)
	if n < cfg.MinLen || n > cfg.MaxLen {
		return false
	}
	for _, r := range rs {
		if r >= 'A' && r <= 'Z' && r != 'D' && r != 'J' { // D and J have no figure
			return false
		}
	}
	rev := []rune(sitor.Reverse(tok))
	if len(rev) != n {
		return false
	}
	letters := false
	for _, r := range rev {
		if r >= 'A' && r <= 'Z' {
			letters = true
		} else if r > 127 || !isMark(byte(r)) {
			return false
		}
	}
	if !letters {
		return false
	}
	w := string(rev)
	for _, e := range c.byLen[n] {
		if match(w, e.word) {
			return true
		}
	}
	return false
}
