package server

import (
	"strings"
	"sync"
	"time"
)

// Limits for viewers (clients that may not change settings, usually arriving through a tunnel from the internet). They keep
// one visitor from using up the computer: too many live connections, or a program asking for log searches in a loop.
// Administrators on the local network are never limited.

// connLimiter counts the live (event stream) connections of viewers, in total and per client address.
type connLimiter struct {
	mu    sync.Mutex
	total int
	per   map[string]int
}

// acquire takes a place for a viewer. It returns a function that gives the place back, or nil when the limits are reached
// (a limit of 0 is no limit).
func (l *connLimiter) acquire(key string, maxTotal, maxPer int) (release func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if maxTotal > 0 && l.total >= maxTotal {
		return nil
	}
	if maxPer > 0 && l.per[key] >= maxPer {
		return nil
	}
	if l.per == nil {
		l.per = map[string]int{}
	}
	l.total++
	l.per[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			if l.per[key]--; l.per[key] <= 0 {
				delete(l.per, key)
			}
		})
	}
}

// rateBurst is how many seconds' worth of requests a viewer may send at once.
const rateBurst = 6

// rateKeys is the most clients remembered at once.
const rateKeys = 4000

type bucket struct {
	tokens float64
	at     time.Time
	warned time.Time // when the system log was last told about this client
}

// rateLimiter is a token bucket per client address. Its zero value is ready to use.
type rateLimiter struct {
	mu  sync.Mutex
	now func() time.Time // for tests; nil = time.Now
	by  map[string]*bucket
}

// allow takes cost tokens from the client's bucket, which holds up to rateBurst*rate and fills at rate per second. It reports
// whether the request may go on, how long to wait if not, and whether this is the first refusal to tell the log about.
func (l *rateLimiter) allow(key string, rate int, cost float64) (ok bool, wait time.Duration, first bool) {
	if rate <= 0 {
		return true, 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.by == nil {
		l.by = map[string]*bucket{}
	}
	capacity := float64(rate * rateBurst)
	b := l.by[key]
	if b == nil {
		if len(l.by) >= rateKeys { // forget the clients whose buckets are full again
			for k, x := range l.by {
				if now.Sub(x.at) > time.Duration(rateBurst)*time.Second {
					delete(l.by, k)
				}
			}
		}
		b = &bucket{tokens: capacity, at: now}
		l.by[key] = b
	}
	b.tokens = min(capacity, b.tokens+now.Sub(b.at).Seconds()*float64(rate))
	b.at = now
	if b.tokens >= cost {
		b.tokens -= cost
		return true, 0, false
	}
	wait = time.Duration((cost - b.tokens) / float64(rate) * float64(time.Second))
	if now.Sub(b.warned) > 10*time.Minute {
		b.warned, first = now, true
	}
	return false, wait, first
}

// requestCost is what a request takes from a viewer's bucket: searches and reports cost more than a page of the log.
func requestCost(path, query string) float64 {
	switch {
	case strings.HasPrefix(path, "/api/report/"), path == "/api/stations/export", path == "/api/messages/locate":
		return 5
	case path == "/api/messages" && strings.Contains(query, "text="):
		return 5
	}
	return 1
}
