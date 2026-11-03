// storebench generates a synthetic message log (sizes and text lengths modelled on a real log) and times the message store on it.
// It is a development tool, not part of the program. Run it from the repository root (it reads internal/autocorr/maritime_en.txt):
//
//	go run ./tools/storebench N dir prep    generate N records into dir (messages.jsonl) if dir holds no log, then open the store once
//	                                         (this converts the JSON-lines file into messages.db)
//	go run ./tools/storebench N dir run     time the operations in a fresh process (it changes dir: adds, deletes, backups)
//
// Allow several GB of scratch disk for a million records (the log, the database, its backups). SKIPTEXT=1 leaves out the text
// searches. To compare with the JSON-lines store of earlier versions, build this file against that store (leave out the part
// between the NEW-STORE ONLY markers).

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"navtex/internal/store"
)

func rss() float64 { // MB, from /proc
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			f := strings.Fields(l)
			kb, _ := strconv.ParseFloat(f[1], 64)
			return kb / 1024
		}
	}
	return 0
}

func words() []string {
	b, _ := os.ReadFile("internal/autocorr/maritime_en.txt")
	var w []string
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" || l[0] == '#' {
			continue
		}
		w = append(w, strings.ToUpper(strings.Fields(l)[0]))
	}
	return w
}

func gen(dir string, n int) {
	os.MkdirAll(dir, 0o755)
	f, _ := os.Create(filepath.Join(dir, "messages.jsonl"))
	bw := bufio.NewWriterSize(f, 1<<20)
	rng := rand.New(rand.NewSource(1))
	ws := words()
	t0 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	span := float64(n) / 300 * 86400 // about 300 records a day
	for i := 1; i <= n; i++ {
		kind, textLen := "message", int(math.Exp(rng.NormFloat64()*1.0+math.Log(570)))
		switch x := rng.Float64(); {
		case x < 0.09:
			kind, textLen = "fragment", 20+rng.Intn(300)
		case x < 0.20:
			kind = "incomplete"
		}
		textLen = min(textLen, 6400)
		var sb strings.Builder
		for sb.Len() < textLen {
			sb.WriteString(ws[rng.Intn(len(ws))])
			sb.WriteByte(' ')
		}
		if rng.Intn(50) == 0 {
			sb.WriteString("GALE WARNING TSUNAMI ")
		}
		text := sb.String()
		rx := t0.Add(time.Duration(float64(i) / float64(n) * span * float64(time.Second)))
		var lines []store.Line
		for len(text) > 0 {
			k := min(len(text), 30)
			if k < len(text) {
				if j := strings.LastIndexByte(text[:k], ' '); j > 0 {
					k = j + 1
				}
			}
			lines = append(lines, store.Line{Time: rx.Add(time.Duration(len(lines)) * time.Second), Quality: store.F2(rng.Float64()), Text: text[:k]})
			text = text[k:]
		}
		r := store.Record{ID: int64(i), Version: 1, Channel: []int{518, 490}[rng.Intn(2)], Rcv: rng.Intn(4), Received: rx, End: rx.Add(time.Duration(len(lines)) * time.Second),
			Kind: kind, B1: string(rune('A' + rng.Intn(26))), B2: string(rune('A' + rng.Intn(26))), Serial: fmt.Sprintf("%02d", 1+rng.Intn(99)), NavArea: 1 + rng.Intn(21),
			StationID: 1 + rng.Intn(120), DistanceKm: rng.Float64() * 5000, BearingDeg: rng.Float64() * 360, Quality: store.F2(rng.Float64()), IdentMethod: "uid", IdentScore: store.F2(rng.Float64() * 5), Lines: lines}
		r.Station = fmt.Sprintf("STATION %d", r.StationID)
		if rng.Intn(10) == 0 {
			r.Station, r.StationID = "UNID", 0
		}
		for c := 0; c < rng.Intn(4); c++ {
			r.Candidates = append(r.Candidates, store.Candidate{StationID: 1 + rng.Intn(120), Label: "CAND", Score: store.F2(rng.Float64() * 5)})
		}
		if kind == "message" {
			var all []string
			for _, l := range lines {
				all = append(all, l.Text)
			}
			r.Corrected = strings.Join(all, "")
		}
		b, _ := json.Marshal(struct {
			Op string        `json:"op"`
			R  *store.Record `json:"r"`
		}{"put", &r})
		bw.Write(b)
		bw.WriteByte('\n')
	}
	bw.Flush()
	f.Close()
}

func timeIt(name string, reps int, fn func()) {
	var d []time.Duration
	for i := 0; i < reps; i++ {
		t := time.Now()
		fn()
		d = append(d, time.Since(t))
	}
	sort.Slice(d, func(a, b int) bool { return d[a] < d[b] })
	fmt.Printf("  %-34s %10.1f ms  (median of %d)\n", name, float64(d[len(d)/2].Microseconds())/1000, reps)
}

func fileMB(paths ...string) float64 {
	var n int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return float64(n) / 1e6
}

func dataMB(dir string) float64 { // the message database (SQLite store) or the message file (JSON-lines store)
	db := filepath.Join(dir, "messages.db")
	if _, err := os.Stat(db); err == nil {
		return fileMB(db, db+"-wal")
	}
	return fileMB(filepath.Join(dir, "messages.jsonl"))
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: storebench N dir prep|run   (run from the repository root; see the comment at the top)")
		os.Exit(2)
	}
	n, _ := strconv.Atoi(os.Args[1])
	dir := os.Args[2]
	t := time.Now()
	if os.Args[3] == "prep" { // generate the log if there is none, then open the store once (the SQLite store converts it here)
		jsonl := filepath.Join(dir, "messages.jsonl")
		if _, err := os.Stat(jsonl); err != nil {
			if _, err := os.Stat(filepath.Join(dir, "messages.db")); err != nil {
				gen(dir, n)
				fmt.Printf("generated %d records in %.1f s\n", n, time.Since(t).Seconds())
			}
		}
		fmt.Printf("== %d records, message file %.0f MB\n", n, fileMB(jsonl))
		t = time.Now()
		s, err := store.Open(dir)
		if err != nil {
			panic(err)
		}
		fmt.Printf("  first open (the SQLite store converts the JSON-lines file here) %8.2f s\n", time.Since(t).Seconds())
		s.Close()
		fmt.Printf("  data now %.0f MB\n", dataMB(dir))
		return
	}
	t = time.Now()
	s, err := store.Open(dir)
	if err != nil {
		panic(err)
	}
	open := time.Since(t)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("  data %.0f MB, %d records\n", dataMB(dir), s.Count())
	fmt.Printf("  open                               %10.2f s    heap %.0f MB, RSS %.0f MB\n", open.Seconds(), float64(ms.HeapAlloc)/1e6, rss())
	day := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(float64(n) / 300 / 2 * 86400 * float64(time.Second)))
	timeIt("Count", 5, func() { s.Count() })
	timeIt("newest 50, no filter", 5, func() { s.Query(store.Filter{Newest: true, Limit: 50}) })
	timeIt("filter B1=F, newest 50", 5, func() { s.Query(store.Filter{B1: "F", Newest: true, Limit: 50}) })
	timeIt("filter NAVAREA=7 + kind message", 5, func() { s.Query(store.Filter{NavArea: 7, Kind: "message", Newest: true, Limit: 50}) })
	timeIt("one day (From/To)", 5, func() { s.Query(store.Filter{From: day, To: day.Add(24 * time.Hour), Limit: 500}) })
	timeIt("max distance 500 km", 5, func() { s.Query(store.Filter{MaxDistance: 500, Newest: true, Limit: 50}) })
	if os.Getenv("SKIPTEXT") == "" {
		timeIt("text search GALE (common-ish)", 3, func() { s.Query(store.Filter{Text: "GALE WARNING", Newest: true, Limit: 50}) })
		timeIt("text search no match", 3, func() { s.Query(store.Filter{Text: "ZZZQQQ", Limit: 50}) })
		runtime.ReadMemStats(&ms)
		fmt.Printf("  (after text searches: sys memory %.0f MB, RSS %.0f MB)\n", float64(ms.Sys)/1e6, rss())
	}
	timeIt("CountByStation", 5, func() { s.CountByStation() })
	{ // a record that is in the filtered list (otherwise Locate gives up at once)
		all, _ := s.Query(store.Filter{B1: "F", Newest: true})
		if len(all) > 0 {
			mid := all[len(all)/2].ID
			timeIt("Locate a record in filtered list", 3, func() {
				if s.Locate(store.Filter{B1: "F", Newest: true}, mid) < 0 {
					panic("record not found")
				}
			})
		}
	}
	id := int64(n / 2)
	timeIt("Get by ID", 5, func() { s.Get(id) })
	var added int
	last, _ := s.Query(store.Filter{Newest: true, Limit: 1})
	timeIt("Add 1000 new records (in time order)", 1, func() {
		for i := 0; i < 1000; i++ {
			r, _ := s.Get(int64(1 + i))
			r.ID = 0
			r.Received = last[0].Received.Add(time.Duration(i+1) * time.Minute)
			r.End = r.Received
			for k := range r.Lines {
				r.Lines[k].Time = r.Received
			}
			if _, err := s.Add(r); err != nil {
				panic(err)
			}
			added++
		}
	})
	timeIt("Delete 100 + Undo", 1, func() {
		ids := make([]int64, 100)
		for i := range ids {
			ids[i] = int64(n/3 + i)
		}
		s.Delete(ids...)
		s.Undo()
	})
	timeIt("Export all (to discard)", 1, func() { s.Export(io.Discard, store.Filter{}) })
	timeIt("Export one NAVAREA", 1, func() { s.Export(io.Discard, store.Filter{NavArea: 7}) })
	timeIt("Backup (VACUUM INTO)", 1, func() { s.Backup("bench", 1) })
	{ // writes and queries while a backup runs
		done := make(chan struct{})
		go func() { s.Backup("bench2", 1); close(done) }()
		var worst, qworst time.Duration
		cnt := 0
		for i := 0; ; i++ {
			select {
			case <-done:
				fmt.Printf("  during that backup: %d Adds, slowest %.1f ms; slowest query %.1f ms\n", cnt, float64(worst.Microseconds())/1000, float64(qworst.Microseconds())/1000)
				goto after
			default:
			}
			r, _ := s.Get(int64(1 + i%1000))
			r.ID = 0
			r.Received = last[0].Received.Add(time.Duration(2000+i) * time.Minute)
			t0 := time.Now()
			s.Add(r)
			if d := time.Since(t0); d > worst {
				worst = d
			}
			t0 = time.Now()
			s.Query(store.Filter{Newest: true, Limit: 50})
			if d := time.Since(t0); d > qworst {
				qworst = d
			}
			cnt++
		}
	}
after:
	timeIt("Delete 20% of the log (one batch)", 1, func() { s.DeleteWhere(store.Filter{NavArea: 3}) })
	// NEW-STORE ONLY begin
	timeIt("Restore 1000 from the trash", 1, func() {
		tr, _ := s.Trash(store.Filter{Limit: 1000})
		ids := make([]int64, len(tr))
		for i, r := range tr {
			ids[i] = r.ID
		}
		s.Restore(ids...)
	})
	// NEW-STORE ONLY end
	timeIt("Compact", 1, func() { s.Compact() })
	fmt.Printf("  data after compact %.0f MB\n", dataMB(dir))
	s.Close()
	t = time.Now()
	s2, err := store.Open(dir)
	if err != nil {
		fmt.Printf("  reopen failed after %.2f s: %v\n", time.Since(t).Seconds(), err)
		return
	}
	fmt.Printf("  reopen after changes               %10.2f s\n", time.Since(t).Seconds())
	s2.Close()
}
