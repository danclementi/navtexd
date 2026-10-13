package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// inputWatch notices a live input that has gone quiet in the wrong way: no samples arriving at all (a frozen USB or
// virtual audio path, a radio that stopped sending), or samples that never change (a muted or disconnected device).
// Either looks like an empty band otherwise. It only reports; restarting the channel is left to the operator.
type inputWatch struct {
	after   time.Duration
	log     func(level, text string)
	lastNew atomic.Int64 // unix nanoseconds of the last block with changing samples
	lastAny atomic.Int64 // unix nanoseconds of the last block of any kind
	warned  bool
}

func newInputWatch(after time.Duration, log func(level, text string)) *inputWatch {
	w := &inputWatch{after: after, log: log}
	now := time.Now().UnixNano()
	w.lastNew.Store(now)
	w.lastAny.Store(now)
	return w
}

// constant reports whether every sample of the block has the same value.
func constant(b []complex64) bool {
	for _, v := range b[1:] {
		if v != b[0] {
			return false
		}
	}
	return true
}

// block is called for every block read from the input.
func (w *inputWatch) block(b []complex64) {
	now := time.Now().UnixNano()
	w.lastAny.Store(now)
	if len(b) > 1 && !constant(b) {
		w.lastNew.Store(now)
	}
}

func (w *inputWatch) run(ctx context.Context) {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			w.check(now)
		}
	}
}

func (w *inputWatch) check(now time.Time) {
	quiet := now.Sub(time.Unix(0, w.lastNew.Load()))
	if quiet < w.after {
		if w.warned {
			w.warned = false
			w.log("info", "input: audio is arriving again")
		}
		return
	}
	if w.warned {
		return
	}
	w.warned = true
	if now.Sub(time.Unix(0, w.lastAny.Load())) >= w.after {
		w.log("warn", fmt.Sprintf("input: no new samples for %.0f s (the input has stopped delivering; check the device or the radio, and restart the channel if needed)", quiet.Seconds()))
	} else {
		w.log("warn", fmt.Sprintf("input: the samples have not changed for %.0f s (muted or disconnected input?)", quiet.Seconds()))
	}
}
