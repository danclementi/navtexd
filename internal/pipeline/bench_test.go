package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"navtex/internal/iq"
	"navtex/internal/navtex"
	"navtex/internal/testvec"
)

// Statistical comparison against stored reference results on extra random seeds. The vectors come from a generator that
// is not part of this repository, so the test is skipped when testdata/bench is absent.
// Score per run = wrong characters vs the truth body; a missing message counts as the whole body.
func TestBenchVsReference(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "bench", "*.json"))
	files = filterJSON(files)
	if len(files) == 0 {
		t.Skip("no bench vectors in testdata/bench")
	}
	type tot struct{ ref, got, refFail, gotFail, n int }
	totals := map[string]*tot{}
	for _, f := range files {
		base := strings.TrimSuffix(filepath.Base(f), ".json")
		var m meta
		testvec.JSON(t, filepath.Join("bench", base+".json"), &m)
		kind := strings.SplitN(base, "S", 2)[0]
		if totals[kind] == nil {
			totals[kind] = &tot{}
		}
		T := totals[kind]
		for ch, info := range m.Channels {
			var chHz float64
			fmt.Sscan(ch, &chHz)
			var p navtex.Parser
			truth := p.Feed(m.Truth[ch])[0]
			full := len(norm(truth.Body))
			refErr := full
			if len(info.Messages) > 0 {
				refErr = lev(norm(info.Messages[0].Body), norm(truth.Body))
			}
			w, err := iq.OpenWav(testvec.Path(t, filepath.Join("bench", base+".wav")), iq.WavOptions{CentreHz: m.CentreHz, HasCentre: true})
			if err != nil {
				t.Fatal(err)
			}
			var got *navtex.Message
			_, err = Run(context.Background(), w, testCfg(chHz), time.Time{}, func(e Event) {
				if e.Kind == EvMessage && e.Message.Complete && got == nil {
					mm := e.Message
					got = &mm
				}
			})
			w.Close()
			if err != nil {
				t.Fatal(err)
			}
			gotErr := full
			if got != nil {
				gotErr = lev(norm(got.Body), norm(truth.Body))
			}
			T.n++
			T.ref += refErr
			T.got += gotErr
			if info.Messages == nil {
				T.refFail++
			}
			if got == nil {
				T.gotFail++
			}
			fmt.Fprintf(os.Stderr, "%-12s %s ref %3d got %3d\n", base, ch, refErr, gotErr)
		}
	}
	for k, T := range totals {
		if float64(T.got) > 1.15*float64(T.ref)+20 {
			t.Errorf("%s: Go %d wrong chars vs reference %d", k, T.got, T.ref)
		}
		t.Logf("%-8s runs %2d | reference: %4d wrong chars, %d no-message | Go: %4d wrong chars, %d no-message",
			k, T.n, T.ref, T.refFail, T.got, T.gotFail)
	}
}

func filterJSON(fs []string) []string {
	var out []string
	for _, f := range fs {
		if !strings.HasSuffix(f, "_filters.json") && !strings.HasSuffix(f, "filters.json") {
			out = append(out, f)
		}
	}
	return out
}
