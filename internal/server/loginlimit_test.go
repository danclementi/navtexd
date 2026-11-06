package server

import (
	"net/http"
	"testing"
	"time"
)

func TestLoginLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := &loginLimiter{now: func() time.Time { return now }}
	for i := 0; i < loginTries-1; i++ {
		if b, _ := l.blocked("a"); b {
			t.Fatalf("blocked after %d wrong tokens", i)
		}
		if locked, _ := l.failed("a"); locked {
			t.Fatalf("locked after %d wrong tokens", i+1)
		}
	}
	if locked, _ := l.failed("a"); !locked {
		t.Fatal("not locked after the allowed number of wrong tokens")
	}
	if b, wait := l.blocked("a"); !b || wait <= 0 || wait > loginLockout {
		t.Fatalf("blocked = %v, wait %v", b, wait)
	}
	if b, _ := l.blocked("other"); b {
		t.Fatal("another client is blocked too")
	}
	now = now.Add(loginLockout + time.Second)
	if b, _ := l.blocked("a"); b {
		t.Fatal("still blocked after the lock-out time")
	}
	// failures spread over more than the window do not add up
	l = &loginLimiter{now: func() time.Time { return now }}
	for i := 0; i < 3*loginTries; i++ {
		if locked, _ := l.failed("slow"); locked {
			t.Fatalf("locked by failures %d windows apart", i)
		}
		now = now.Add(loginWindow + time.Second)
	}
	// a right token clears the count
	l = &loginLimiter{now: func() time.Time { return now }}
	for i := 0; i < loginTries-1; i++ {
		l.failed("b")
	}
	l.ok("b")
	if locked, _ := l.failed("b"); locked {
		t.Fatal("locked although a right token came in between")
	}
}

func TestLoginLimiterGlobal(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := &loginLimiter{now: func() time.Time { return now }}
	var global bool
	for i := 0; i < loginGlobal && !global; i++ { // many different clients, each below its own limit
		_, global = l.failed(string(rune('A'+i%26)) + string(rune('a'+i/26)))
	}
	if !global {
		t.Fatal("the global limit did not trigger")
	}
	if b, _ := l.blocked("a-new-client"); !b {
		t.Fatal("a new client is not refused while the global lock-out lasts")
	}
}

func TestLoginKey(t *testing.T) {
	r, _ := http.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	if k := loginKey(r); k != "127.0.0.1" {
		t.Fatalf("key %q", k)
	}
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	if k := loginKey(r); k != "203.0.113.9" {
		t.Fatalf("key %q with the Cloudflare header", k)
	}
}
