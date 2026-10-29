package compare

import (
	"fmt"
	"sync"
	"time"
)

// Msg is a finished message (or incomplete message) from one side.
type Msg struct {
	Key        string // identification group, e.g. "EA78"; messages without one are not compared
	Start, End time.Time
	Text       string // body text as received (raw)
}

// Config sets the rules.
type Config struct {
	NameA, NameB string        // shown in the results
	Pair         time.Duration // two messages are the same one when their starts are this close
	Wait         time.Duration // how long to wait for the other side's copy before calling it missing
	MinDiffs     int           // fewer differences than this are not reported
}

// Result is one reportable difference.
type Result struct {
	Key     string
	Start   time.Time // earliest start of the message on either side
	End     time.Time // latest end
	Summary string
	Detail  string
	Missing string // name of the side that has no copy ("" when both have one)
	Diffs   int
}

type pending struct {
	m    Msg
	seen time.Time
}

// Comparer pairs the messages of two sides. It is safe for concurrent use.
type Comparer struct {
	mu   sync.Mutex
	cfg  Config
	side [2][]pending
}

func New(cfg Config) *Comparer {
	if cfg.MinDiffs < 1 {
		cfg.MinDiffs = 1
	}
	return &Comparer{cfg: cfg}
}

// Add offers a finished message from side 0 (A) or 1 (B); now is the current time. It returns a result when the
// message completes a pair that differs.
func (c *Comparer) Add(side int, m Msg, now time.Time) []Result {
	if m.Key == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	o := 1 - side
	for i, p := range c.side[o] {
		if p.m.Key != m.Key || absDur(p.m.Start.Sub(m.Start)) > c.cfg.Pair {
			continue
		}
		c.side[o] = append(c.side[o][:i], c.side[o][i+1:]...)
		a, b := m, p.m
		if side == 1 {
			a, b = p.m, m
		}
		if r, ok := c.compare(a, b); ok {
			return []Result{r}
		}
		return nil
	}
	c.side[side] = append(c.side[side], pending{m, now})
	return nil
}

// Expire reports the messages that have waited longer than the configured time for the other side's copy; present
// says whether each side (0, 1) is running, so that nothing is reported missing from a side that is not.
func (c *Comparer) Expire(now time.Time, present [2]bool) []Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Result
	for s := 0; s < 2; s++ {
		keep := c.side[s][:0]
		for _, p := range c.side[s] {
			if now.Sub(p.seen) < c.cfg.Wait {
				keep = append(keep, p)
				continue
			}
			if !present[1-s] {
				continue
			}
			have, lack := c.name(s), c.name(1-s)
			r := Result{Key: p.m.Key, Start: p.m.Start, End: p.m.End, Missing: lack, Diffs: 1 << 20}
			r.Summary = fmt.Sprintf("%s: only %s has this message, %s has none", p.m.Key, have, lack)
			out = append(out, r)
		}
		c.side[s] = keep
	}
	return out
}

func (c *Comparer) name(s int) string {
	if s == 0 {
		return c.cfg.NameA
	}
	return c.cfg.NameB
}

func (c *Comparer) compare(a, b Msg) (Result, bool) {
	na, nb := Normalize(a.Text), Normalize(b.Text)
	ops, ok := Align(na, nb)
	r := Result{Key: a.Key, Start: minT(a.Start, b.Start), End: maxT(a.End, b.End)}
	if !ok {
		r.Diffs = 1 << 20
		r.Summary = fmt.Sprintf("%s: the texts are too different to align (%d and %d characters)", a.Key, len(na), len(nb))
		return r, true
	}
	if len(ops) < c.cfg.MinDiffs {
		return r, false
	}
	r.Diffs = len(ops)
	r.Summary = fmt.Sprintf("%s: %s", a.Key, Describe(ops, c.cfg.NameA, c.cfg.NameB))
	r.Detail = Excerpts(na, nb, ops, c.cfg.NameA, c.cfg.NameB, 40)
	return r, true
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
func minT(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
