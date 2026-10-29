package app

import (
	"fmt"
	"strings"
	"time"

	"navtex/internal/compare"
	"navtex/internal/config"
	"navtex/internal/store"
)

// comparer returns the comparer for the current options (nil when the comparison is off), making a new one when the
// options have changed.
func (r *Runner) comparer() (*compare.Comparer, config.Compare) {
	cfg := r.Config()
	cp := cfg.Compare
	r.cmpMu.Lock()
	defer r.cmpMu.Unlock()
	if !cp.Enabled {
		r.cmp = nil
		return nil, cp
	}
	if r.cmp == nil || r.cmpCfg != cp {
		r.cmpCfg = cp
		r.cmp = compare.New(compare.Config{NameA: cfg.Label(cp.ChannelA - 1), NameB: cfg.Label(cp.ChannelB - 1),
			Pair: secs(cp.PairS), Wait: secs(cp.WaitS), MinDiffs: cp.MinDiffs})
	}
	return r.cmp, cp
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// compareAdd offers a stored record to the channel comparison.
func (r *Runner) compareAdd(ch int, rec store.Record) {
	cmp, cp := r.comparer()
	if cmp == nil || rec.Kind == "fragment" || rec.B1 == "" {
		return
	}
	side := -1
	switch ch + 1 {
	case cp.ChannelA:
		side = 0
	case cp.ChannelB:
		side = 1
	}
	if side < 0 {
		return
	}
	var body []string
	for i, l := range rec.Lines {
		t := strings.TrimSpace(l.Text)
		if i == 0 && strings.HasPrefix(t, "ZCZC") || t == "NNNN" {
			continue
		}
		body = append(body, t)
	}
	m := compare.Msg{Key: fmt.Sprintf("%d %s%s%s", rec.Channel, rec.B1, rec.B2, rec.Serial), Start: rec.Received, End: rec.End, Text: strings.Join(body, "\n")}
	for _, res := range cmp.Add(side, m, time.Now()) {
		r.compareReport(cp, res)
	}
}

// compareLoop reports the messages one channel never produced.
func (r *Runner) compareLoop() {
	for range time.Tick(5 * time.Second) {
		cmp, cp := r.comparer()
		if cmp == nil {
			continue
		}
		present := [2]bool{r.State(cp.ChannelA - 1).Running, r.State(cp.ChannelB - 1).Running}
		for _, res := range cmp.Expire(time.Now(), present) {
			r.compareReport(cp, res)
		}
	}
}

// compareReport logs a difference and saves a clip of the signal that produced it.
func (r *Runner) compareReport(cp config.Compare, res compare.Result) {
	cfg := r.Config()
	na, nb := cfg.Label(cp.ChannelA-1), cfg.Label(cp.ChannelB-1)
	r.log("warn", fmt.Sprintf("COMPARE %s vs %s: %s", na, nb, res.Summary))
	cl := cfg.Clips
	if cl.BeforeS+cl.AfterS <= 0 {
		r.log("info", "COMPARE: no clip saved, the clip function is off (options: clips)")
		return
	}
	if span := time.Since(res.Start).Seconds(); span > cl.BeforeS && span < 3600 { // (a replayed file has its own time line)
		r.log("info", fmt.Sprintf("COMPARE: the message began %.0f s ago, the clip keeps %.0f s: the start is not in it (options: clips)", span, cl.BeforeS))
	}
	note := fmt.Sprintf("%s vs %s: %s; message from %s to %s UTC", na, nb, res.Summary, res.Start.UTC().Format("15:04:05"), res.End.UTC().Format("15:04:05"))
	r.requestClip([]int{cp.ChannelA - 1, cp.ChannelB - 1}, note, res.Detail, 0)
}

// requestClip asks the first of the channels that has a clip buffer for a clip. A clip already being recorded delays
// the request until it is done (a few tries).
func (r *Runner) requestClip(chans []int, note, detail string, try int) {
	var lastErr error
	for _, ch := range chans {
		ring := r.chains[ch].ring.Load()
		if ring == nil {
			continue
		}
		wait, err := ring.RequestNote(note, detail)
		if err == nil {
			r.logc(ch, "info", fmt.Sprintf("COMPARE: audio clip requested, saving in %.0f s", wait))
			return
		}
		lastErr = err
	}
	if lastErr != nil && try < 4 {
		time.AfterFunc(secs(r.Config().Clips.AfterS+2), func() { r.requestClip(chans, note, detail, try+1) })
		return
	}
	r.log("warn", "COMPARE: no clip saved: "+func() string {
		if lastErr != nil {
			return lastErr.Error()
		}
		return "neither channel is running"
	}())
}
