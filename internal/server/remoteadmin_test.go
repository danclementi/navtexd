package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"navtex/internal/config"
)

const remoteToken = "0123456789abcdefghij" // 20 characters

func remoteSetup(t *testing.T, on bool) (tunnel *httptest.Server, s *Server) {
	t.Helper()
	cfg := config.Default()
	cfg.Server.Token = remoteToken
	cfg.Server.RemoteAdmin = on
	s, _, _, _ = setup(t, cfg)
	tunnel = httptest.NewServer(s.handler(true))
	t.Cleanup(tunnel.Close)
	return tunnel, s
}

func tokHdr(tok string) map[string]string { return map[string]string{"X-Navtex-Token": tok} }

func TestRemoteAdminOffByDefault(t *testing.T) {
	tunnel, _ := remoteSetup(t, false)
	if code, _ := do(t, tunnel, "GET", "/api/settings", nil, tokHdr(remoteToken)); code != 403 {
		t.Fatalf("settings with the right token while remote_admin is off: %d, want 403", code)
	}
	code, b := do(t, tunnel, "POST", "/api/login", map[string]any{"token": remoteToken}, nil)
	if code != 403 {
		t.Fatalf("login while remote_admin is off: %d %v", code, b)
	}
	if _, b := do(t, tunnel, "GET", "/api/state", nil, nil); b["can_login"] != false {
		t.Fatalf("can_login = %v while remote_admin is off", b["can_login"])
	}
}

func TestRemoteAdminLoginAndLimits(t *testing.T) {
	tunnel, _ := remoteSetup(t, true)
	if _, b := do(t, tunnel, "GET", "/api/state", nil, nil); b["can_login"] != true || b["role"] != "viewer" {
		t.Fatalf("a viewer should be offered the login: %v", b)
	}
	// the login sets a cookie
	body, _ := json.Marshal(map[string]any{"token": remoteToken})
	req, _ := http.NewRequest("POST", tunnel.URL+"/api/login", bytes.NewReader(body))
	req.Header.Set("X-Navtex", "1")
	req.Header.Set("Cf-Visitor", `{"scheme":"https"}`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "navtex_token" {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || !ck.Secure || ck.MaxAge != 30*24*3600 || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v (want HttpOnly, Secure behind https, 30 days, SameSite=Strict)", ck)
	}
	cookie := map[string]string{"Cookie": "navtex_token=" + ck.Value}

	_, b := do(t, tunnel, "GET", "/api/state", nil, cookie)
	if b["role"] != "admin" || b["remote"] != true || b["can_login"] != false {
		t.Fatalf("hello with the cookie: %v", b)
	}
	code, st := do(t, tunnel, "GET", "/api/settings", nil, cookie)
	if code != 200 {
		t.Fatalf("settings as remote administrator: %d", code)
	}
	for _, f := range st["fields"].([]any) {
		m := f.(map[string]any)
		if m["path"] == "server.token" && m["value"] == remoteToken {
			t.Fatal("the token itself was sent to a remote administrator")
		}
	}
	// ordinary settings may be changed, the server's own may not
	if code, b := do(t, tunnel, "PUT", "/api/settings", map[string]any{"changes": map[string]any{"operator.comment": "remote"}}, cookie); code != 200 {
		t.Fatalf("an ordinary setting: %d %v", code, b)
	}
	for _, p := range []string{"server.listen", "server.token", "server.remote_admin", "server.admin_networks", "clips.dir", "decoder.extra_words_file", "extra.0.input.file.path", "input.sdrconnect.address"} {
		code, b := do(t, tunnel, "PUT", "/api/settings", map[string]any{"changes": map[string]any{p: "x"}}, cookie)
		if code != 403 {
			t.Errorf("%s: %d %v (want 403: local network only)", p, code, b)
		}
	}
	// the wrong token gets nothing
	if code, _ := do(t, tunnel, "GET", "/api/settings", nil, tokHdr("wrong")); code != 403 {
		t.Fatalf("a wrong token: %d", code)
	}
	// logout clears the cookie
	req, _ = http.NewRequest("POST", tunnel.URL+"/api/logout", nil)
	req.Header.Set("X-Navtex", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cleared := false
	for _, c := range resp.Cookies() {
		cleared = cleared || (c.Name == "navtex_token" && c.MaxAge < 0)
	}
	if resp.StatusCode != 200 || !cleared {
		t.Fatalf("logout: %d, cookie cleared %v", resp.StatusCode, cleared)
	}
}

// Wrong tokens sent in a header or cookie to any endpoint count like wrong tokens in the login box.
func TestRemoteAdminGuessingViaHeader(t *testing.T) {
	tunnel, _ := remoteSetup(t, true)
	for i := 0; i < 30; i++ {
		do(t, tunnel, "GET", "/api/settings", nil, tokHdr("guess"+strconv.Itoa(i)))
	}
	if code, _ := do(t, tunnel, "GET", "/api/settings", nil, tokHdr(remoteToken)); code != 403 {
		t.Fatalf("the right token after 30 wrong guesses: %d, want 403 (locked out)", code)
	}
	if code, _ := do(t, tunnel, "POST", "/api/login", map[string]any{"token": remoteToken}, nil); code != 429 {
		t.Fatalf("login after 30 wrong guesses: %d, want 429", code)
	}
}

// A browser that still holds the cookie of an old token sends it with every request: that must not lock the owner out.
func TestRemoteAdminStaleCookieDoesNotLockOut(t *testing.T) {
	tunnel, _ := remoteSetup(t, true)
	stale := map[string]string{"Cookie": "navtex_token=an-old-token-value"}
	for i := 0; i < 40; i++ {
		do(t, tunnel, "GET", "/api/state", nil, stale)
	}
	if code, _ := do(t, tunnel, "POST", "/api/login", map[string]any{"token": remoteToken}, nil); code != 200 {
		t.Fatalf("login after 40 requests with a stale cookie: %d", code)
	}
}

func TestRemoteAdminNeedsLongToken(t *testing.T) {
	c := config.Default()
	c.Server.RemoteAdmin = true
	c.Server.Token = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("remote_admin with a 5 character token was accepted")
	}
	c.Server.Token = remoteToken
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIsHTTPS(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	if isHTTPS(r) {
		t.Fatal("plain request taken as https")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !isHTTPS(r) {
		t.Fatal("X-Forwarded-Proto not honoured")
	}
	r, _ = http.NewRequest("GET", "/", nil)
	r.Header.Set("Cf-Visitor", `{"scheme":"https"}`)
	if !isHTTPS(r) {
		t.Fatal("Cf-Visitor not honoured")
	}
}
