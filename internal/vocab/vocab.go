// Package vocab builds and reads the maritime vocabulary used for word autocorrection (docs/SERVER_DESIGN.md, section 7).
// The vocabulary is a plain text file, one word per line, optionally followed by a count; '#' starts a comment. Users
// can add words by hand.
package vocab

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Options select which words are collected.
type Options struct {
	MinLen, MaxLen int // word length in letters (YaND corrects words of 5 to 16 characters)
	MinLines       int // a corpus word must occur in at least this many different clean lines
}

// DefaultOptions are the YaND word lengths and a word seen in at least three different lines.
func DefaultOptions() Options { return Options{MinLen: 5, MaxLen: 16, MinLines: 3} }

// CleanLine reports whether a received line is free of reception errors: no unreadable-character marks ('~', '*', '?')
// or lost-shift marks ("{}"), and only characters that NAVTEX can carry as text.
func CleanLine(s string) bool {
	if strings.Contains(s, "{}") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ':
		case strings.ContainsRune(`.,:;/-+()'=%&"`, r):
		default:
			return false
		}
	}
	return true
}

// noise matches the fixed-pattern letter groups that the receiver prints for idle/phasing signals and other
// non-text ("WUXQPZ", "QOXPRZ": five letters with X third and Z last) which are error-free lines but not words.
func noise(w string) bool { return len(w) == 6 && w[2] == 'X' && w[5] == 'Z' }

// Words returns the words of a line: runs of capital letters that are not touching a digit.
func Words(s string, o Options) []string {
	var out []string
	for i := 0; i < len(s); {
		if s[i] < 'A' || s[i] > 'Z' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] >= 'A' && s[j] <= 'Z' {
			j++
		}
		touching := i > 0 && s[i-1] >= '0' && s[i-1] <= '9' || j < len(s) && s[j] >= '0' && s[j] <= '9'
		if n := j - i; !touching && n >= o.MinLen && n <= o.MaxLen && !noise(s[i:j]) {
			out = append(out, s[i:j])
		}
		i = j
	}
	return out
}

type stat struct{ occ, lines int }

// Counter collects word statistics from received lines.
type Counter struct {
	o     Options
	words map[string]*stat
	seen  map[string]bool // distinct clean lines already counted (stations repeat their messages)

	Lines, Clean, Distinct int // lines offered, clean ones, distinct clean ones
}

// NewCounter makes an empty counter.
func NewCounter(o Options) *Counter {
	return &Counter{o: o, words: map[string]*stat{}, seen: map[string]bool{}}
}

// AddLine offers one received line. Identical lines count once for "lines" (the repeats of a broadcast would
// otherwise make a single message look like many), and every time for "occurrences".
func (c *Counter) AddLine(text string) {
	c.Lines++
	text = strings.TrimSpace(text)
	if !CleanLine(text) {
		return
	}
	c.Clean++
	fresh := !c.seen[text]
	if fresh {
		c.seen[text] = true
		c.Distinct++
	}
	done := map[string]bool{}
	for _, w := range Words(text, c.o) {
		st := c.words[w]
		if st == nil {
			st = &stat{}
			c.words[w] = st
		}
		st.occ++
		if fresh && !done[w] {
			st.lines++
		}
		done[w] = true
	}
}

// Entry is a vocabulary word with the number of times it was seen.
type Entry struct {
	Word  string
	Count int
}

// Vocabulary is a set of words.
type Vocabulary map[string]int

// Result returns the corpus words that pass the thresholds.
func (c *Counter) Result() Vocabulary {
	v := Vocabulary{}
	for w, st := range c.words {
		if st.lines >= c.o.MinLines {
			v[w] = st.occ
		}
	}
	return v
}

// Seen returns how often a word occurred in the clean lines, whatever the thresholds.
func (c *Counter) Seen(w string) int {
	if st := c.words[w]; st != nil {
		return st.occ
	}
	return 0
}

// Merge adds the words of other (counts are added).
func (v Vocabulary) Merge(other Vocabulary) {
	for w, n := range other {
		v[w] += n
	}
}

// Sorted lists the words alphabetically.
func (v Vocabulary) Sorted() []Entry {
	out := make([]Entry, 0, len(v))
	for w, n := range v {
		out = append(out, Entry{w, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Word < out[j].Word })
	return out
}

// Write saves the vocabulary; comment lines (each without the leading '#') come first.
func (v Vocabulary) Write(w io.Writer, comments ...string) error {
	bw := bufio.NewWriter(w)
	for _, c := range comments {
		fmt.Fprintf(bw, "# %s\n", c)
	}
	for _, e := range v.Sorted() {
		fmt.Fprintf(bw, "%s\t%d\n", e.Word, e.Count)
	}
	return bw.Flush()
}

// Read loads a vocabulary file (word, optionally followed by a count).
func Read(r io.Reader) (Vocabulary, error) {
	v := Vocabulary{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Fields(line)
		n := 0
		if len(f) > 1 {
			n, _ = strconv.Atoi(f[1])
		}
		v[strings.ToUpper(f[0])] += n
	}
	return v, sc.Err()
}

// Load reads a vocabulary file by name.
func Load(path string) (Vocabulary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}
