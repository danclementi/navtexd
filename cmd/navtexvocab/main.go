// navtexvocab builds the maritime vocabulary for word autocorrection from received text.
//
// Sources (any number, any mix):
//
//	-vocab FILE   an existing vocabulary file (words are included as they are)
//	-store FILE   our message store (messages.jsonl): the stored lines
//	-text FILE    plain text, one received line per line
//
// A corpus word is kept when it has 5 to 16 letters and occurs in at least -min-lines different error-free lines
// (no '~', '*', '?' or "{}"); lines are counted once however often a station repeats them. Words taken as they are
// from -vocab are always kept.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"navtex/internal/vocab"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func main() {
	var vfiles, stores, texts list
	flag.Var(&vfiles, "vocab", "existing vocabulary file; may be repeated")
	flag.Var(&stores, "store", "our message store (messages.jsonl); may be repeated")
	flag.Var(&texts, "text", "plain text file, one received line per line; may be repeated")
	o := vocab.DefaultOptions()
	flag.IntVar(&o.MinLen, "min-len", o.MinLen, "shortest word, letters")
	flag.IntVar(&o.MaxLen, "max-len", o.MaxLen, "longest word, letters")
	flag.IntVar(&o.MinLines, "min-lines", o.MinLines, "a corpus word must occur in this many different clean lines")
	minQ := flag.Float64("min-q", 0, "ignore lines whose signal quality is below this (0-100; lines without a quality are kept)")
	out := flag.String("o", "", "output vocabulary file (default: standard output)")
	newOnly := flag.String("new", "", "also write the corpus words that are not in -vocab to this file, most frequent first (for review)")
	flag.Parse()
	if len(stores)+len(texts)+len(vfiles) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, "navtexvocab:", err)
		os.Exit(1)
	}

	cnt := vocab.NewCounter(o)
	for _, f := range stores {
		if err := readStore(f, *minQ, cnt); err != nil {
			fail(fmt.Errorf("%s: %w", f, err))
		}
	}
	for _, f := range texts {
		fh, err := os.Open(f)
		if err != nil {
			fail(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			cnt.AddLine(sc.Text())
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			fail(fmt.Errorf("%s: %w", f, err))
		}
	}
	corpus := cnt.Result()

	known := vocab.Vocabulary{}
	for _, f := range vfiles {
		v, err := vocab.Load(f)
		if err != nil {
			fail(err)
		}
		known.Merge(v)
	}
	// words taken as they are must still look like words
	for w := range known {
		if len(vocab.Words(w, vocab.Options{MinLen: 1, MaxLen: 64})) != 1 {
			delete(known, w)
		}
	}
	result := vocab.Vocabulary{}
	for w := range known {
		result[w] = cnt.Seen(w) + known[w]
	}
	var fresh []vocab.Entry
	for w, n := range corpus {
		if _, ok := known[w]; !ok {
			fresh = append(fresh, vocab.Entry{Word: w, Count: n})
		}
		result[w] = n
	}
	sort.Slice(fresh, func(i, j int) bool {
		if fresh[i].Count != fresh[j].Count {
			return fresh[i].Count > fresh[j].Count
		}
		return fresh[i].Word < fresh[j].Word
	})

	comments := []string{
		"NAVTEX maritime vocabulary for word autocorrection, built by navtexvocab",
		fmt.Sprintf("corpus: %d lines, %d error-free, %d different; words of %d-%d letters in at least %d different lines",
			cnt.Lines, cnt.Clean, cnt.Distinct, o.MinLen, o.MaxLen, o.MinLines),
		fmt.Sprintf("%d words (%d from the lexicon / vocabulary files, %d new from the corpus). Format: WORD, tab, times seen (0 = not seen in the corpus). Add words by hand freely.",
			len(result), len(known), len(fresh)),
	}
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fail(err)
		}
		defer f.Close()
		w = f
	}
	if err := result.Write(w, comments...); err != nil {
		fail(err)
	}
	if *newOnly != "" {
		f, err := os.Create(*newOnly)
		if err != nil {
			fail(err)
		}
		bw := bufio.NewWriter(f)
		for _, e := range fresh {
			fmt.Fprintf(bw, "%s\t%d\n", e.Word, e.Count)
		}
		bw.Flush()
		f.Close()
	}
	for _, c := range comments {
		fmt.Fprintln(os.Stderr, c)
	}
}

// readStore feeds the lines of our message store. Records are JSON lines {"op":"put","r":{"lines":[{"q":..,"x":..}]}};
// later puts of the same id replace earlier ones, and deletes remove them.
func readStore(path string, minQ float64, cnt *vocab.Counter) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	type line struct {
		Q float64 `json:"q"`
		X string  `json:"x"`
	}
	type rec struct {
		ID    int    `json:"id"`
		Lines []line `json:"lines"`
	}
	recs := map[int]rec{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var op struct {
			Op string `json:"op"`
			ID int    `json:"id"`
			R  rec    `json:"r"`
		}
		if err := json.Unmarshal(sc.Bytes(), &op); err != nil {
			continue
		}
		switch op.Op {
		case "put":
			recs[op.R.ID] = op.R
		case "del", "delete":
			delete(recs, op.ID)
			delete(recs, op.R.ID)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	ids := make([]int, 0, len(recs))
	for id := range recs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		for _, l := range recs[id].Lines {
			if l.Q >= minQ {
				cnt.AddLine(l.X)
			}
		}
	}
	return nil
}
