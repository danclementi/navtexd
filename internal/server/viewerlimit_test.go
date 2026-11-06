package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"navtex/internal/config"
)

func TestConnLimiter(t *testing.T) {
	var l connLimiter
	a1 := l.acquire("a", 3, 2)
	a2 := l.acquire("a", 3, 2)
	if a1 == nil || a2 == nil {
		t.Fatal("the first two connections of one address were refused")
	}
	if l.acquire("a", 3, 2) != nil {
		t.Fatal("a third connection of one address was accepted (limit 2)")
	}
	b1 := l.acquire("b", 3, 2)
	if b1 == nil {
		t.Fatal("another address was refused although the total allows it")
	}
	if l.acquire("c", 3, 2) != nil {
		t.Fatal("the total limit (3) was not enforced")
	}
	a1()
	a1() // releasing twice must not give two places back
	c1 := l.acquire("c", 3, 2)
	if c1 == nil {
		t.Fatal("a place was not given back")
	}
	if l.acquire("d", 3, 2) != nil {
		t.Fatal("a double release gave back too many places")
	}
	for _, f := range []func(){a2, b1, c1} {
		f()
	}
	if l.total != 0 || len(l.per) != 0 {
		t.Fatalf("not empty after all released: %d %v", l.total, l.per)
	}
	var u connLimiter // 0 = no limit
	for i := 0; i < 100; i++ {
		if u.acquire("x", 0, 0) == nil {
			t.Fatal("refused although there is no limit")
		}
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := &rateLimiter{now: func() time.Time { return now }}
	const rate = 10 // bucket of 60
	for i := 0; i < 60; i++ {
		if ok, _, _ := l.allow("a", rate, 1); !ok {
			t.Fatalf("request %d of the burst was refused", i)
		}
	}
	ok, wait, first := l.allow("a", rate, 1)
	if ok || wait <= 0 || !first {
		t.Fatalf("after the burst: ok=%v wait=%v first=%v", ok, wait, first)
	}
	if _, _, first := l.allow("a", rate, 1); first {
		t.Fatal("the log would be told again at once")
	}
	if ok, _, _ := l.allow("b", rate, 1); !ok {
		t.Fatal("another client is limited too")
	}
	now = now.Add(time.Second) // 10 tokens back
	for i := 0; i < 10; i++ {
		if ok, _, _ := l.allow("a", rate, 1); !ok {
			t.Fatalf("request %d after a second was refused", i)
		}
	}
	if ok, _, _ := l.allow("a", rate, 1); ok {
		t.Fatal("more than the refill was allowed")
	}
	// a search costs more
	now = now.Add(time.Second)
	if ok, _, _ := l.allow("a", rate, 5); !ok {
		t.Fatal("a search was refused with 10 tokens available")
	}
	if ok, _, _ := l.allow("a", rate, 5); !ok {
		t.Fatal("the second search was refused with 5 tokens available")
	}
	if ok, _, _ := l.allow("a", rate, 5); ok {
		t.Fatal("a third search was allowed")
	}
	if ok, _, _ := l.allow("z", 0, 1000); !ok { // 0 = no limit
		t.Fatal("limited although the rate is 0")
	}
}

func TestRequestCost(t *testing.T) {
	for _, c := range []struct {
		path, q string
		want    float64
	}{
		{"/api/messages", "limit=100", 1},
		{"/api/messages", "text=storm&limit=100", 5},
		{"/api/report/list", "", 5},
		{"/api/stations/export", "", 5},
		{"/api/state", "", 1},
	} {
		if got := requestCost(c.path, c.q); got != c.want {
			t.Errorf("%s?%s costs %v, want %v", c.path, c.q, got, c.want)
		}
	}
}

// Viewers coming through the tunnel listener are limited; the local network is not.
func TestViewerLimitsApply(t *testing.T) {
	cfg := config.Default()
	cfg.Server.MaxViewers = 2
	cfg.Server.MaxPerViewer = 2
	cfg.Server.ViewerRate = 5 // bucket of 30
	s, _, _, _ := setup(t, cfg)
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	local := httptest.NewServer(s.handler(false))
	defer local.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open := func(url string) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "GET", url+"/api/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	var held []*http.Response
	for i := 0; i < 2; i++ {
		r := open(tunnel.URL)
		if r.StatusCode != 200 {
			t.Fatalf("viewer connection %d: %d", i, r.StatusCode)
		}
		held = append(held, r)
	}
	r := open(tunnel.URL)
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable || r.Header.Get("Retry-After") == "" {
		t.Fatalf("a third viewer connection: %d (want 503 with Retry-After)", r.StatusCode)
	}
	for i := 0; i < 5; i++ { // the local network is never limited
		r := open(local.URL)
		if r.StatusCode != 200 {
			t.Fatalf("local connection %d: %d", i, r.StatusCode)
		}
		held = append(held, r)
	}
	held[0].Body.Close() // a viewer leaves: the place is free again (after the server notices)
	deadline := time.Now().Add(3 * time.Second)
	for {
		r := open(tunnel.URL)
		if r.StatusCode == 200 {
			held = append(held, r)
			break
		}
		r.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("the place of a viewer that left was not given back")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, h := range held {
		h.Body.Close()
	}

	limited := 0
	for i := 0; i < 60; i++ {
		resp, err := http.Get(tunnel.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited++
			if resp.Header.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
		}
	}
	if limited < 20 {
		t.Fatalf("only %d of 60 quick requests from a viewer were limited", limited)
	}
	for i := 0; i < 100; i++ {
		resp, err := http.Get(local.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("local request %d: %d", i, resp.StatusCode)
		}
	}
}

func TestClientAddrIgnoresJunkHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.7:5555"
	r.Header.Set("X-Real-Ip", "1.2.3.4/../../x?\nforged line")
	r.Header.Set("X-Forwarded-For", "not an address, 5.6.7.8")
	if ip, proxied := clientAddr(r); ip != "192.0.2.7" || proxied {
		t.Errorf("junk headers were taken: %q %v", ip, proxied)
	}
	r.Header.Set("Cf-Connecting-Ip", "2001:db8::1")
	if ip, proxied := clientAddr(r); ip != "2001:db8::1" || !proxied {
		t.Errorf("%q %v", ip, proxied)
	}
	g := newPlacer()
	if g.lookup(context.Background(), "10.0.0.1") != "" || g.lookup(context.Background(), "x/../y") != "" {
		t.Error("lookup of a local or invalid address")
	}
}
