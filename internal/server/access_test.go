package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"navtex/internal/config"
)

func req(remote, host string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/api/state", nil)
	r.RemoteAddr, r.Host = remote, host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestClassify(t *testing.T) {
	set := config.Server{PublicHosts: "navtex.example.org", AdminNetworks: "100.64.0.0/10", Hosts: "shack.example.net"}
	a := NewAccess(func() config.Server { return set })
	cases := []struct {
		name   string
		r      *http.Request
		tunnel bool
		want   Role
	}{
		{"loopback", req("127.0.0.1:5000", "127.0.0.1:8080", nil), false, Admin},
		{"loopback v6", req("[::1]:5000", "localhost:8080", nil), false, Admin},
		{"lan", req("192.168.1.20:5000", "192.168.1.5:8080", nil), false, Admin},
		{"lan mapped v6", req("[::ffff:10.0.0.7]:5000", "shack:8080", nil), false, Admin},
		{"admin net", req("100.100.1.1:5000", "100.100.1.9:8080", nil), false, Admin},
		{"internet", req("8.8.8.8:5000", "x:8080", nil), false, Viewer},
		{"tunnel listener", req("127.0.0.1:5000", "navtex.example.org", nil), true, Viewer},
		{"cloudflared to main port", req("127.0.0.1:5000", "navtex.example.org", map[string]string{"Cf-Connecting-Ip": "1.2.3.4"}), false, Viewer},
		{"forwarded", req("127.0.0.1:5000", "127.0.0.1:8080", map[string]string{"X-Forwarded-For": "1.2.3.4"}), false, Viewer},
		{"public host only", req("127.0.0.1:5000", "navtex.example.org", nil), false, Viewer},
		{"rebinding", req("127.0.0.1:5000", "evil.example.com:8080", nil), false, Viewer},
		{"configured host", req("192.168.1.9:5000", "shack.example.net", nil), false, Admin},
	}
	for _, c := range cases {
		if got := a.Classify(c.r, c.tunnel); got.Role != c.want {
			t.Errorf("%s: role %v (%s), want %v", c.name, got.Role, got.Why, c.want)
		}
	}
	set.Token = "s3cret"
	if id := a.Classify(req("127.0.0.1:5000", "localhost", nil), false); id.Role != Viewer || !id.NeedToken {
		t.Errorf("token missing: %+v", id)
	}
	if id := a.Classify(req("127.0.0.1:5000", "localhost", map[string]string{"X-Navtex-Token": "s3cret"}), false); id.Role != Admin {
		t.Errorf("token given: %+v", id)
	}
	if id := a.Classify(req("127.0.0.1:5000", "localhost", map[string]string{"X-Navtex-Token": "nope"}), false); id.Role != Viewer {
		t.Errorf("wrong token: %+v", id)
	}
}

func TestCheckWrite(t *testing.T) {
	ok := req("127.0.0.1:1", "localhost:8080", map[string]string{"X-Navtex": "1", "Origin": "http://localhost:8080"})
	if err := CheckWrite(ok); err != nil {
		t.Error(err)
	}
	for name, h := range map[string]map[string]string{
		"no header":  {"Origin": "http://localhost:8080"},
		"bad origin": {"X-Navtex": "1", "Origin": "http://evil.example"},
		"cross site": {"X-Navtex": "1", "Sec-Fetch-Site": "cross-site"},
	} {
		if CheckWrite(req("127.0.0.1:1", "localhost:8080", h)) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
