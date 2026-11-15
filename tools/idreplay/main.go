// Command idreplay replays logged messages through the station identifier and compares the result with the station that the
// log says sent them. It measures what a change of the station table or of the thresholds does to the identification rate
// and to the rate of wrong stations. It is a development tool.
//
//	idreplay -stations navtexd_stations.txt -jsonl 490.jsonl -jsonl 518.jsonl [options]
//
// The messages come from JSON-lines exports (the Log tab's Export, or tools/yandimport for YaND databases). Only complete
// messages (kind "message") are used. A message with a station in the log is a test: the station must be found in the table by
// channel, B1 letter and call sign. The station of the log is the truth, so the results show agreement with the log; if the log
// itself was made with the same keywords (YaND's was), a high score partly confirms that.
//
// Three situations are replayed: B1 as received, B1 treated as not read (time of day and keywords only), and, with -kw, the same
// with extra keywords added to the table. Messages that the log left unidentified have no truth; they are only counted.
//
//	-cut DATE       test the messages received from this date on (default 2023-10-01); a keyword list should come from
//	                messages before it
//	-n N            test a random sample of N messages (default 30000, 0 = all)
//	-kw FILE        extra keywords, tab-separated: channel kHz, B1, call, word, messages, coverage (0-1), concentration (0-1),
//	                1 if the word looks like a name else 0. Selected with -conc, -minn, -cap, -names, -uid
//	-thresholds     also sweep minKeyword and minMargin
//	-review FILE    write a CSV of messages that the log left unidentified and that the table with -kw names but the table
//	                without does not: time, channel, B1, station and the keywords that matched (no message text)
//	-review-n N     number of rows for -review (default 50)
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"navtex/internal/ident"
	"navtex/internal/stations"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

type rec struct {
	freq  float64
	b1    string
	when  time.Time
	text  string
	truth int // index into the table, -1 when the log has no station
}

type kwrow struct {
	freq     float64
	b1, call string
	word     string
	n        int
	cov, con float64
	name     bool
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "idreplay:", err)
	os.Exit(1)
}

func baseCall(s string) string { return strings.SplitN(s, "-", 2)[0] }

// load reads the messages received at or after cut. With unid false the messages with a station are returned, else those without.
func load(files []string, tab *stations.Table, cut time.Time, unid bool) []rec {
	var out []rec
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			fail(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<28)
		for sc.Scan() {
			var w struct {
				R struct {
					Ch    int    `json:"ch"`
					Rx    string `json:"rx"`
					Kind  string `json:"kind"`
					B1    string `json:"b1"`
					St    string `json:"st"`
					Lines []struct {
						X string `json:"x"`
					} `json:"lines"`
				} `json:"r"`
			}
			if json.Unmarshal(sc.Bytes(), &w) != nil {
				continue
			}
			r := w.R
			if r.Kind != "message" || (r.St == "UNID" || r.St == "") != unid {
				continue
			}
			rx, err := time.Parse(time.RFC3339Nano, r.Rx)
			if err != nil || rx.Before(cut) {
				continue
			}
			fk := float64(r.Ch) / 1000
			truth := -1
			if !unid {
				if len(r.B1) != 1 {
					continue
				}
				call, n := strings.SplitN(r.St, " ", 2)[0], 0
				for i, s := range tab.Stations {
					if s.FreqKHz == fk && s.ID == r.B1 && (s.Callsign == call || baseCall(s.Callsign) == baseCall(call)) {
						truth, n = i, n+1
					}
				}
				if n != 1 { // not in the table, or not clear which row
					continue
				}
			}
			var sb strings.Builder
			for _, l := range r.Lines {
				if strings.HasPrefix(l.X, "ZCZC") || l.X == "NNNN" {
					continue
				}
				sb.WriteString(l.X)
				sb.WriteByte('\n')
			}
			out = append(out, rec{fk, r.B1, rx, sb.String(), truth})
		}
		f.Close()
	}
	return out
}

func loadKW(name string) []kwrow {
	f, err := os.Open(name)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	var out []kwrow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 8 {
			continue
		}
		fk, _ := strconv.ParseFloat(p[0], 64)
		n, _ := strconv.Atoi(p[4])
		cov, _ := strconv.ParseFloat(p[5], 64)
		con, _ := strconv.ParseFloat(p[6], 64)
		out = append(out, kwrow{fk, p[1], p[2], strings.ToUpper(p[3]), n, cov, con, p[7] == "1"})
	}
	return out
}

type selection struct {
	conc     float64
	minN     int
	nameOnly bool
	uidAt    float64 // words with at least this concentration go to the UID list (0 = all to GID)
	cap      int     // most words added per station
}

// build copies the table and adds the selected keywords. added tells, per station, which words are new.
func build(base *stations.Table, kw []kwrow, c selection) (*stations.Table, map[int]map[string]bool) {
	t := &stations.Table{Version: base.Version, Stations: append([]stations.Station(nil), base.Stations...)}
	for i := range t.Stations {
		t.Stations[i].GID = append([]string(nil), t.Stations[i].GID...)
		t.Stations[i].UID = append([]string(nil), t.Stations[i].UID...)
	}
	added := map[int]map[string]bool{}
	if kw == nil {
		return t, added
	}
	per := map[int][]kwrow{}
	for _, k := range kw {
		if k.con < c.conc || k.n < c.minN || (c.nameOnly && !k.name) {
			continue
		}
		for i, s := range t.Stations {
			if s.FreqKHz == k.freq && s.ID == k.b1 && (s.Callsign == k.call || baseCall(s.Callsign) == baseCall(k.call)) {
				per[i] = append(per[i], k)
			}
		}
	}
	for i, l := range per {
		sort.Slice(l, func(a, b int) bool { return l[a].cov > l[b].cov })
		have := map[string]bool{}
		for _, w := range append(append([]string(nil), t.Stations[i].GID...), t.Stations[i].UID...) {
			have[strings.ToUpper(strings.TrimSpace(w))] = true
		}
		n := 0
		for _, k := range l {
			if have[k.word] {
				continue
			}
			if n++; n > c.cap {
				break
			}
			if added[i] == nil {
				added[i] = map[string]bool{}
			}
			added[i][k.word] = true
			if c.uidAt > 0 && k.con >= c.uidAt {
				t.Stations[i].UID = append(t.Stations[i].UID, k.word)
			} else {
				t.Stations[i].GID = append(t.Stations[i].GID, k.word)
			}
		}
	}
	return t, added
}

type tally struct{ n, named, right, wrong, unnamed, inCand int }

// run identifies the messages (B1 as received, or with B1 treated as not read) and compares the result with the log.
func run(id *ident.Identifier, recs []rec, noB1 bool) tally {
	var mu sync.Mutex
	var tot tally
	var wg sync.WaitGroup
	const workers = 4
	chunk := (len(recs) + workers - 1) / workers
	for a := 0; a < len(recs); a += chunk {
		b := min(a+chunk, len(recs))
		wg.Add(1)
		go func(part []rec) {
			defer wg.Done()
			var t tally
			for _, r := range part {
				in := ident.Input{FreqKHz: r.freq, B1: r.b1, When: r.when, Text: r.text}
				if noB1 {
					in.B1 = "?"
				}
				res := id.Identify(in)
				t.n++
				switch {
				case res.Index < 0:
					t.unnamed++
					for _, c := range res.Candidates {
						if c.Index == r.truth {
							t.inCand++
							break
						}
					}
				case res.Index == r.truth:
					t.named++
					t.right++
				default:
					t.named++
					t.wrong++
				}
			}
			mu.Lock()
			tot.n += t.n
			tot.named += t.named
			tot.right += t.right
			tot.wrong += t.wrong
			tot.unnamed += t.unnamed
			tot.inCand += t.inCand
			mu.Unlock()
		}(recs[a:b])
	}
	wg.Wait()
	return tot
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func report(label string, t tally) {
	fmt.Printf("%-52s n=%6d  right %5.1f%%  wrong %4.1f%% (of named %4.1f%%)  unnamed %5.1f%%  truth offered in %4.1f%% of unnamed\n",
		label, t.n, pct(t.right, t.n), pct(t.wrong, t.n), pct(t.wrong, t.named), pct(t.unnamed, t.n), pct(t.inCand, t.unnamed))
}

func main() {
	var files list
	tabFile := flag.String("stations", "", "station table (navtexd_stations.txt)")
	flag.Var(&files, "jsonl", "message export (JSON lines); may be repeated")
	cutS := flag.String("cut", "2023-10-01", "test the messages received from this date on")
	n := flag.Int("n", 30000, "random sample of this many test messages (0 = all)")
	kwFile := flag.String("kw", "", "extra keywords to try (tab-separated, see the command help)")
	sweep := flag.Bool("thresholds", false, "sweep minKeyword and minMargin")
	conc := flag.Float64("conc", 0.9, "-kw: least concentration at the station (0-1)")
	minN := flag.Int("minn", 8, "-kw: least number of messages")
	capN := flag.Int("cap", 20, "-kw: most words added per station")
	names := flag.Bool("names", true, "-kw: only words that look like names")
	uid := flag.Float64("uid", 0, "-kw: words with at least this concentration go to the UID list (0 = all to GID)")
	review := flag.String("review", "", "write the review sample (CSV) to this file")
	reviewN := flag.Int("review-n", 50, "rows of the review sample")
	flag.Parse()
	if *tabFile == "" || len(files) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	cut, err := time.Parse("2006-01-02", *cutS)
	if err != nil {
		fail(err)
	}
	f, err := os.Open(*tabFile)
	if err != nil {
		fail(err)
	}
	tab, err := stations.Parse(f)
	f.Close()
	if err != nil {
		fail(err)
	}
	recs := load(files, tab, cut, false)
	rand.New(rand.NewSource(7)).Shuffle(len(recs), func(i, j int) { recs[i], recs[j] = recs[j], recs[i] })
	fmt.Printf("messages with a station since %s: %d", *cutS, len(recs))
	if *n > 0 && len(recs) > *n {
		recs = recs[:*n]
		fmt.Printf(" (a sample of %d is tested)", *n)
	}
	fmt.Println()
	var kw []kwrow
	if *kwFile != "" {
		kw = loadKW(*kwFile)
	}
	sel := selection{conc: *conc, minN: *minN, nameOnly: *names, uidAt: *uid, cap: *capN}
	baseTab, _ := build(tab, nil, sel)
	kwTab, added := build(tab, kw, sel)
	type setup struct {
		name string
		id   *ident.Identifier
	}
	sets := []setup{{"current table", ident.New(baseTab)}}
	if kw != nil {
		sets = append(sets, setup{fmt.Sprintf("with keywords (conc>=%.2f n>=%d cap %d)", *conc, *minN, *capN), ident.New(kwTab)})
	}
	var shared []rec
	for _, r := range recs {
		k := 0
		for _, s := range tab.Stations {
			if s.FreqKHz == r.freq && s.ID == r.b1 {
				k++
			}
		}
		if k > 1 {
			shared = append(shared, r)
		}
	}
	fmt.Printf("of which the B1 letter is shared by two or more stations of the channel: %d\n", len(shared))
	for _, s := range sets {
		fmt.Println()
		report(s.name+": B1 as received", run(s.id, recs, false))
		report(s.name+": B1 letter shared", run(s.id, shared, false))
		report(s.name+": B1 not read", run(s.id, recs, true))
	}
	if *sweep {
		k0, m0 := ident.Thresholds()
		defer ident.SetThresholds(k0, m0)
		fmt.Println("\nthresholds (keyword points, margin), B1 not read:")
		for _, s := range sets {
			for _, th := range [][2]float64{{4, 2}, {3, 1.5}, {2, 0.75}, {1.5, 0.5}, {1, 0.25}} {
				ident.SetThresholds(th[0], th[1])
				report(fmt.Sprintf("%s  %.1f / %.2f", s.name, th[0], th[1]), run(s.id, recs, true))
			}
		}
		ident.SetThresholds(k0, m0)
	}
	if kw != nil || *review != "" {
		unids := load(files, tab, cut, true)
		withB1 := 0
		for _, r := range unids {
			if len(r.b1) == 1 {
				withB1++
			}
		}
		fmt.Printf("\nmessages that the log left unidentified since %s: %d (%d with a B1 letter); no truth is known\n", *cutS, len(unids), withB1)
		type row struct {
			r   rec
			res ident.Result
		}
		var gained []row
		for _, s := range sets {
			named := 0
			for _, r := range unids {
				in := ident.Input{FreqKHz: r.freq, B1: r.b1, When: r.when, Text: r.text}
				a := s.id.Identify(in)
				if a.Index >= 0 {
					named++
					if len(sets) > 1 && s.name != sets[0].name && sets[0].id.Identify(in).Index < 0 {
						gained = append(gained, row{r, a})
					}
				}
			}
			fmt.Printf("%-52s names %5d (%.1f%%)\n", s.name, named, pct(named, len(unids)))
		}
		if *review != "" && len(gained) > 0 {
			rand.New(rand.NewSource(11)).Shuffle(len(gained), func(i, j int) { gained[i], gained[j] = gained[j], gained[i] })
			if len(gained) > *reviewN {
				gained = gained[:*reviewN]
			}
			sort.Slice(gained, func(i, j int) bool { return gained[i].r.when.Before(gained[j].r.when) })
			out, err := os.Create(*review)
			if err != nil {
				fail(err)
			}
			w := csv.NewWriter(out)
			w.Write([]string{"received_utc", "channel_kHz", "B1", "proposed_call", "proposed_station", "method", "score", "matched_keywords", "new_keywords_among_them", "message_chars"})
			for _, g := range gained {
				c := g.res.Candidates[0]
				var all, fresh []string
				for _, t := range c.Terms {
					if (t.Kind == "uid" || t.Kind == "gid") && t.Keyword != "" && t.Points > 0 {
						all = append(all, strings.TrimSpace(t.Keyword))
						if added[c.Index][strings.ToUpper(strings.TrimSpace(t.Keyword))] {
							fresh = append(fresh, strings.TrimSpace(t.Keyword))
						}
					}
				}
				st := kwTab.Stations[g.res.Index]
				w.Write([]string{g.r.when.Format("2006-01-02 15:04"), stations.FreqString(g.r.freq), g.r.b1, st.Callsign, st.Name, g.res.Method,
					fmt.Sprintf("%.1f", g.res.Score), strings.Join(all, " "), strings.Join(fresh, " "), strconv.Itoa(len(g.r.text))})
			}
			w.Flush()
			out.Close()
			fmt.Printf("review sample of %d rows written to %s\n", len(gained), *review)
		}
	}
}
