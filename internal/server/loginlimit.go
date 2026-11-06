package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limits on wrong access tokens. A client that is not on the local network (a viewer arriving through a tunnel, say) may try
// tokens only so often; without this, a long token would still be open to a program trying millions of guesses.
const (
	loginTries    = 5                // wrong tokens one client may send within loginWindow
	loginGlobal   = 40               // wrong tokens all remote clients together may send within loginWindow
	loginWindow   = 10 * time.Minute // the period the failures are counted over
	loginLockout  = 15 * time.Minute // how long a client (or, when the global limit is hit, every remote client) is refused afterwards
	loginMaxKeys  = 2000             // the most clients remembered at once
	loginBlockMsg = "too many wrong tokens"
)

type loginFails struct {
	n     int       // wrong tokens since first
	first time.Time // the first of them
	until time.Time // refused until this time (zero = not locked)
}

// loginLimiter counts wrong tokens per client and in total. Its zero value is ready to use.
type loginLimiter struct {
	mu     sync.Mutex
	now    func() time.Time // for tests; nil = time.Now
	by     map[string]*loginFails
	global loginFails
}

func (l *loginLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// loginKey names the client of a request: the address Cloudflare reports when the request came through it (the peer is then the
// tunnel program on this computer, the same for everybody), else the peer address.
func loginKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); v != "" {
		return v
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// blocked reports whether the client may not try a token now, and for how long that lasts.
func (l *loginLimiter) blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if now.Before(l.global.until) {
		return true, l.global.until.Sub(now)
	}
	if f := l.by[key]; f != nil && now.Before(f.until) {
		return true, f.until.Sub(now)
	}
	return false, 0
}

// failed records a wrong token. It reports whether this one locked the client (first) or every remote client (global).
func (l *loginLimiter) failed(key string) (locked, global bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	count := func(f *loginFails) {
		if f.n == 0 || now.Sub(f.first) > loginWindow {
			f.n, f.first = 0, now
		}
		f.n++
	}
	count(&l.global)
	if l.global.n >= loginGlobal {
		l.global.until, l.global.n = now.Add(loginLockout), 0
		global = true
	}
	if l.by == nil {
		l.by = map[string]*loginFails{}
	}
	if len(l.by) >= loginMaxKeys { // forget the clients whose time is over
		for k, f := range l.by {
			if now.After(f.until) && now.Sub(f.first) > loginWindow {
				delete(l.by, k)
			}
		}
		if len(l.by) >= loginMaxKeys { // all busy: the global limit is what protects now
			return false, global
		}
	}
	f := l.by[key]
	if f == nil {
		f = &loginFails{}
		l.by[key] = f
	}
	count(f)
	if f.n >= loginTries {
		f.until, f.n = now.Add(loginLockout), 0
		locked = true
	}
	return locked, global
}

// ok forgets the wrong tokens of a client that then got it right.
func (l *loginLimiter) ok(key string) {
	l.mu.Lock()
	delete(l.by, key)
	l.mu.Unlock()
}
