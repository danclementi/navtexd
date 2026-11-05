package server

// Review tests (not part of the project). See the review report.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"navtex/internal/config"
)

// Finding A1: identification scores include 0.6*exp(-d/2000) for the distance d from the receiver to each candidate station
// (ident.go score()). The record detail (/api/messages/{id}) sends "cand" (candidates with scores) and "is" (the score)
// to viewers unchanged, so a viewer sees numbers that depend on the operator position: with candidates at several known
// stations the position can be triangulated. CLAUDE.md rule 6: viewers never see distance or bearing.
func TestReview_ViewerScoresDependOnPosition(t *testing.T) {
	get := func(lat, lon float64) string {
		cfg := config.Default()
		cfg.Input.File.Path = "/nonexistent.wav"
		cfg.Input.Source = "file"
		cfg.Operator.LatDeg, cfg.Operator.LonDeg = lat, lon
		s, _, st, _ := setup(t, cfg)
		viewer := httptest.NewServer(s.handler(true))
		defer viewer.Close()
		at := time.Date(2026, 9, 28, 0, 50, 0, 0, time.UTC)
		r := addMsg(t, st, s.cat, "E", at, "NOTHING USEFUL")
		resp, err := http.Get(viewer.URL + "/api/messages/" + strconv.FormatInt(r.ID, 10))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		b, _ := json.Marshal(map[string]any{"cand": m["cand"], "is": m["is"]})
		return string(b)
	}
	a := get(36.85, -76.3) // Norfolk
	b := get(51.5, -0.1)   // London
	t.Logf("viewer sees, receiver at Norfolk: %s", a)
	t.Logf("viewer sees, receiver at London:  %s", b)
	if a != b {
		t.Fatal("a viewer's record detail changes with the operator position (identification scores carry the distance term)")
	}
}

// Finding A2: a reverse proxy on the same machine that adds none of the listed forwarding headers (nginx "proxy_pass" with no
// proxy_set_header lines, a TCP forwarder, ssh -R, stunnel) makes every remote client an administrator unless server.token is
// set: the peer is 127.0.0.1 and the Host the proxy sends is an IP literal. CheckWrite only stops browsers; a script sends
// X-Navtex: 1 and no Origin. Shown here with the request nginx sends by default.
func TestReview_HeaderlessLocalProxyIsAdmin(t *testing.T) {
	a := NewAccess(func() config.Server { return config.Default().Server })
	r := httptest.NewRequest("GET", "/api/settings", nil)
	r.RemoteAddr = "127.0.0.1:51514"
	r.Host = "127.0.0.1:8080"                 // nginx default: Host $proxy_host
	r.Header.Set("Connection", "close")      // nginx default
	r.Header.Set("User-Agent", "attacker/1") // from the internet
	id := a.Classify(r, false)
	if id.Role == Admin {
		t.Fatalf("a request relayed by a header-less local proxy is %s (%s): every internet client is an administrator", id.Role, id.Why)
	}
	// and a write passes CheckWrite when sent by a script
	r2 := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(nil))
	r2.Header.Set("X-Navtex", "1")
	if err := CheckWrite(r2); err != nil {
		t.Log("CheckWrite refused", err)
	}
}

// Finding A3: /api/login has no rate limit or delay, is reachable by every role (viewers, tunnel), and answers at once;
// a remote client can try tokens as fast as the network allows. Shown: 300 wrong guesses in a row are all answered 403
// with no slow-down or lock-out.
func TestReview_LoginNoRateLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Token = "s3cret"
	s, _, _, _ := setup(t, cfg)
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	start := time.Now()
	for i := 0; i < 300; i++ {
		req, _ := http.NewRequest("POST", tunnel.URL+"/api/login", strings.NewReader(`{"token":"guess`+strconv.Itoa(i)+`"}`))
		req.Header.Set("X-Navtex", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("guess %d: %d", i, resp.StatusCode)
		}
	}
	if d := time.Since(start); d < 3*time.Second {
		t.Fatalf("300 wrong tokens answered in %v from the tunnel listener: no throttling of token guesses", d)
	}
}
