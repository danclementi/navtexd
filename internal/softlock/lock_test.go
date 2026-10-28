package softlock

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The regression oracle: the soft bit streams of the navtex-rs test vectors (captured from the Python reference decoder)
// go straight into the lock, which must show the same locked state, hypothesis, release, switch, pending and emitted
// counts at every comparison, and the same characters and text. Vectors from https://github.com/bgm60/navtex-rs
// (GPL-3.0-or-later), reduced to what the lock needs (see testdata).

// vectors holds testdata/vectors.tar.gz: per set the files bits.txt.gz, lock_<variant>_trace.txt.gz,
// lock_<variant>_chars.csv and decoded_<variant>.txt.
func vectors(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := os.Open("testdata/vectors.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(z)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimPrefix(h.Name, "./")] = b
	}
	return files
}

func gzLines(t *testing.T, b []byte) []string {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(z)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

type traceRow struct{ locked, h, releases, switches, pending, emitted int }

func readTrace(t *testing.T, path string, b []byte) []traceRow {
	var rows []traceRow
	for _, l := range gzLines(t, b) {
		f := strings.Split(l, ",")
		if len(f) != 6 {
			t.Fatalf("%s: bad row %q", path, l)
		}
		var v [6]int
		for i := range v {
			n, err := strconv.Atoi(f[i])
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			v[i] = n
		}
		rows = append(rows, traceRow{v[0], v[1], v[2], v[3], v[4], v[5]})
	}
	return rows
}

type wantChar struct {
	bit int
	ch  rune
}

func readChars(b []byte) []wantChar {
	var out []wantChar
	for i, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if i == 0 {
			continue
		}
		f := strings.Split(strings.TrimSpace(l), ",")
		bit, _ := strconv.Atoi(f[1])
		cp, _ := strconv.Atoi(strings.TrimSpace(f[2]))
		out = append(out, wantChar{bit, rune(cp)})
	}
	return out
}

func TestMatchesReferenceLock(t *testing.T) {
	files := vectors(t)
	var sets []string
	for name := range files {
		if set, ok := strings.CutSuffix(name, "/bits.txt.gz"); ok {
			sets = append(sets, set)
		}
	}
	sort.Strings(sets)
	if len(sets) == 0 {
		t.Fatal("no test vectors")
	}
	variants := []struct {
		name string
		p    Params
	}{{"core", CoreParams()}, {"follow", FollowParams()}, {"full", DefaultParams()}}
	var chars, releases, switches, ownRel, runDrops, ghostDrops int
	for _, v := range variants {
		for _, set := range sets {
			var soft []float64
			for _, l := range gzLines(t, files[set+"/bits.txt.gz"]) {
				x, err := strconv.ParseFloat(l, 64)
				if err != nil {
					t.Fatal(err)
				}
				soft = append(soft, x)
			}
			tp := set + "/lock_" + v.name + "_trace.txt.gz"
			want := readTrace(t, tp, files[tp])
			lk := New(v.p)
			var got []Char
			seen := 0
			for _, s := range soft {
				before := lk.Releases()
				out := lk.Push(s)
				for _, c := range out {
					if c.Releases != before {
						t.Fatalf("%s %s: release number of a character", v.name, set)
					}
				}
				got = append(got, out...)
				if lk.Comparisons() > seen {
					w := want[seen]
					what := fmt.Sprintf("%s %s comparison %d", v.name, set, seen)
					if (w.locked == 1) != lk.Locked() {
						t.Fatalf("locked: %s", what)
					}
					if lk.Hypothesis() != w.h {
						t.Fatalf("hypothesis %d, want %d: %s", lk.Hypothesis(), w.h, what)
					}
					if lk.Releases() != w.releases || lk.Switches() != w.switches || lk.Pending() != w.pending || lk.Emitted() != w.emitted {
						t.Fatalf("counts rel %d/%d sw %d/%d pend %d/%d em %d/%d: %s", lk.Releases(), w.releases, lk.Switches(), w.switches, lk.Pending(), w.pending, lk.Emitted(), w.emitted, what)
					}
					seen++
				}
			}
			if seen != len(want) {
				t.Fatalf("%s %s: %d comparisons, want %d", v.name, set, seen, len(want))
			}
			got = append(got, lk.Finish()...)
			wc := readChars(files[set+"/lock_"+v.name+"_chars.csv"])
			if len(got) != len(wc) {
				t.Fatalf("%s %s: %d characters, want %d", v.name, set, len(got), len(wc))
			}
			var text strings.Builder
			for i := range got {
				if got[i].BitIndex != wc[i].bit || got[i].Ch != wc[i].ch {
					t.Fatalf("%s %s: character %d is %q at %d, want %q at %d", v.name, set, i, got[i].Ch, got[i].BitIndex, wc[i].ch, wc[i].bit)
				}
				if got[i].Ch != '\r' {
					text.WriteRune(got[i].Ch)
				}
			}
			wt := files[set+"/decoded_"+v.name+".txt"]
			if text.String() != strings.ReplaceAll(string(wt), "\r", "") {
				t.Fatalf("%s %s: decoded text differs", v.name, set)
			}
			chars += len(got)
			releases += lk.Releases()
			switches += lk.Switches()
			ownRel += lk.OwnReleases()
			runDrops += lk.LetterRunDrops()
			ghostDrops += lk.GhostRunsDropped()
		}
	}
	if chars == 0 || releases == 0 || switches == 0 || ownRel == 0 || runDrops == 0 || ghostDrops == 0 {
		t.Fatalf("the vectors did not exercise everything: chars %d releases %d switches %d own %d runs %d ghosts %d", chars, releases, switches, ownRel, runDrops, ghostDrops)
	}
	t.Logf("matched: %d characters, %d releases (%d by the own-alignment rule), %d switches, %d letter runs and %d idle runs dropped", chars, releases, ownRel, switches, runDrops, ghostDrops)
}

func TestSilenceNeverLocks(t *testing.T) {
	lk := New(CoreParams())
	n := 0
	for i := 0; i < 20000; i++ {
		n += len(lk.Push(0))
	}
	n += len(lk.Finish())
	if n != 0 || lk.Locked() || lk.Releases() != 0 || lk.Comparisons() != 1425 {
		t.Fatalf("silence: %d chars, locked %v, releases %d, comparisons %d", n, lk.Locked(), lk.Releases(), lk.Comparisons())
	}
}

func TestHelpers(t *testing.T) {
	if a, b := slipNeighbours(0); a != 2 || b != 13 {
		t.Errorf("slipNeighbours(0) = %d, %d", a, b)
	}
	if a, b := slipNeighbours(13); a != 0 || b != 11 {
		t.Errorf("slipNeighbours(13) = %d, %d", a, b)
	}
	if a, b := slipNeighbours(6); a != 8 || b != 4 {
		t.Errorf("slipNeighbours(6) = %d, %d", a, b)
	}
	// the second polarity block slips within itself
	if a, b := slipNeighbours(14 + 13); a != 14+0 || b != 14+11 {
		t.Errorf("slipNeighbours(27) = %d, %d", a, b)
	}
	for _, c := range [][3]int{{-3, 7, 0}, {-7, 7, -1}, {-8, 7, -1}, {0, 7, 0}, {1, 7, 1}, {7, 7, 1}, {8, 7, 2}} {
		if g := ceilDiv(c[0], c[1]); g != c[2] {
			t.Errorf("ceilDiv(%d,%d) = %d, want %d", c[0], c[1], g, c[2])
		}
	}
	if k := bestSplit([]float64{1, 1, 1, 0, 0}, []float64{0, 0, 0, 1, 1}); k != 3 {
		t.Errorf("bestSplit = %d", k)
	}
	if rotate(0b1111000, 1) != 0b1110001 || rotate(0b0110011, 2) != 0b1001101 {
		t.Error("rotate")
	}
	for k := uint(1); k <= 6; k++ {
		if !isIdleLookalike(rotate(Phasing1, k), rotate(Phasing2, k)) {
			t.Errorf("idle look-alike %d", k)
		}
	}
	if isIdleLookalike(0b1110001, 0b1110001) {
		t.Error("two copies of A are not idle")
	}
	n := 0
	for _, c := range trafficCodes {
		if weight(c) != 4 {
			t.Error("weight")
		}
		n++
	}
	if n != 32 {
		t.Errorf("traffic codes: %d", n)
	}
}
