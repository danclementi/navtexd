package server

// Review tests (not part of the project). See the review report.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"navtex/internal/config"
)

// Findings A1 and A2 of the review are accepted, not fixed (see docs/REVIEW_FINDINGS.md and CLAUDE.md rule 6): identification
// scores may let a viewer estimate the distance to the receiver, and a local reverse proxy that adds no forwarding header is
// taken as a local client (a tunnel should point at server.tunnel_listen). Their tests were removed.

// Finding A3: /api/login had no rate limit or delay, is reachable by every role (viewers, tunnel), and answered at once; a remote
// client could try tokens as fast as the network allows. Fixed: after a few wrong tokens a remote client is refused (429) for a
// while, and then not even the right token is accepted from it.
func TestReview_LoginNoRateLimit(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Token = "s3cret-s3cret-s3cret"
	cfg.Server.RemoteAdmin = true
	s, _, _, _ := setup(t, cfg)
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	post := func(tok string) int {
		req, _ := http.NewRequest("POST", tunnel.URL+"/api/login", strings.NewReader(`{"token":"`+tok+`"}`))
		req.Header.Set("X-Navtex", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	refused := 0
	for i := 0; i < 300; i++ {
		switch c := post("guess" + strconv.Itoa(i)); c {
		case 403:
		case 429:
			refused++
		default:
			t.Fatalf("guess %d: %d", i, c)
		}
	}
	if refused < 250 {
		t.Fatalf("only %d of 300 wrong tokens were refused for trying too often", refused)
	}
	if c := post("s3cret-s3cret-s3cret"); c != 429 {
		t.Fatalf("the right token after a burst of wrong ones: %d, want 429 (locked out)", c)
	}
}
