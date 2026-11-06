package app

// Review tests (not part of the project). See the review report.

import (
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"navtex/internal/config"
	"navtex/internal/engine"
	"navtex/internal/store"
)

func reviewWAV(t *testing.T, dir string, seconds float64) string {
	const rate = 48000
	n := int(rate * seconds)
	data := make([]byte, n*4)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < n*2; i++ {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(int16(rng.Intn(2000)-1000)))
	}
	var h []byte
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+len(data)))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 1)
	h = binary.LittleEndian.AppendUint16(h, 2)
	h = binary.LittleEndian.AppendUint32(h, rate)
	h = binary.LittleEndian.AppendUint32(h, rate*4)
	h = binary.LittleEndian.AppendUint16(h, 4)
	h = binary.LittleEndian.AppendUint16(h, 16)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(data)))
	p := filepath.Join(dir, "rev_20260101_000000_516000HZ.wav")
	if err := os.WriteFile(p, append(h, data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Pass 4 stress: restarts, setting changes and engine commands from several goroutines while two channels share one input.
// Run with -race. Also checks that no goroutines are left after Stop.
func TestReview_RunnerRestartStress(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Input.Source = "file"
	cfg.Input.File.Path = reviewWAV(t, dir, 20)
	cfg.Input.File.Realtime = true
	cfg.Extra[0].Enabled = true
	cfg.EditChannel(1, func(c *config.Config) {
		c.Input.Source = "file"
		c.Input.SharedFrom = 1
		c.Channel.FrequencyHz = 518000
	})
	if err := cfg.Validate(); err != nil {
		t.Skip("config not accepted:", err)
	}
	before := runtime.NumGoroutine()
	r := New(dir, cfg, st, Hooks{})
	r.Start()
	var wg sync.WaitGroup
	stop := time.Now().Add(3 * time.Second)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; time.Now().Before(stop); i++ {
				switch (g + i) % 5 {
				case 0:
					r.Restart()
				case 1:
					r.Modify(func(c *config.Config) error { c.Operator.Comment = time.Now().String(); return nil })
				case 2:
					r.Do(i%2, func(e *engine.Engine) { e.Resync() })
				case 3:
					_ = r.State(i % 2)
					_ = r.Scope(i % 2)
				case 4:
					r.SetAutotune(i%2, i%3 == 0)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(g)
	}
	wg.Wait()
	time.Sleep(300 * time.Millisecond)
	s1 := r.State(1)
	r.Stop()
	t.Logf("channel 2 state after the stress: %+v", s1)
	time.Sleep(500 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines: %d before, %d after Stop\n%s", before, after, buf[:n])
	}
}

// Finding R1: Restart is not serialized. stopChain clears c.stop/c.done under the lock and then waits without it, so a second,
// concurrent Restart sees "nothing running", does not wait, and starts a new run while the old one is still finishing. The old
// run's deferred clean-up then overwrites the new run's state (shown as stopped while it runs) and clears c.tap / c.ring /
// c.scope that the new run set; two sources of the same channel are open at once (a sound device or radio opened twice).
func TestReview_ConcurrentRestartLeavesWrongState(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Input.Source = "file"
	cfg.Input.File.Path = reviewWAV(t, dir, 60)
	cfg.Input.File.Realtime = true
	cfg.Extra[0].Enabled = true
	cfg.EditChannel(1, func(c *config.Config) {
		c.Input.Source = "file"
		c.Input.SharedFrom = 1
		c.Channel.FrequencyHz = 518000
	})
	r := New(dir, cfg, st, Hooks{})
	r.Start()
	defer r.Stop()
	for round := 0; round < 40; round++ {
		var wg sync.WaitGroup
		for k := 0; k < 3; k++ {
			wg.Add(1)
			go func() { defer wg.Done(); r.Restart() }()
		}
		wg.Wait()
		time.Sleep(400 * time.Millisecond) // every channel has opened its input by now
		s0, s1 := r.State(0), r.State(1)
		tap := r.chains[0].tap.Load()
		if !s0.Running || !s1.Running || tap == nil {
			t.Fatalf("round %d: after concurrent restarts: ch1 running=%v ch2 running=%v (%q %q), owner tap set=%v",
				round, s0.Running, s1.Running, s1.Info, s1.Error, tap != nil)
		}
	}
}

// Finding (suspected in the report): when the channel that owns a shared input stops on an internal error it starts again by
// itself after 3 s, but a channel that shares its input saw the end of the input, finished normally and stayed stopped.
// A panic is provoked on the owner (through a command, which runs in its decoding goroutine).
func TestReview_SharedChannelRestartsWithItsOwner(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Input.Source = "file"
	cfg.Input.File.Path = reviewWAV(t, dir, 120)
	cfg.Input.File.Realtime = true
	cfg.Extra[0].Enabled = true
	cfg.EditChannel(1, func(c *config.Config) {
		c.Input.Source = "file"
		c.Input.SharedFrom = 1
		c.Channel.FrequencyHz = 518000
	})
	r := New(dir, cfg, st, Hooks{})
	r.Start()
	defer r.Stop()
	waitRunning := func(since time.Time, what string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			a, b := r.State(0), r.State(1)
			if a.Running && b.Running && a.Since.After(since) && b.Since.After(since) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s: channel 1 %+v, channel 2 %+v", what, r.State(0), r.State(1))
	}
	waitRunning(time.Time{}, "start")
	panicAt := time.Now()
	r.Do(0, func(e *engine.Engine) { panic("provoked by the test") })
	waitRunning(panicAt, "after the owner's internal error both channels should run again")
}
