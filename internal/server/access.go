// Package server is the network face of navtexd: an HTTP server with a JSON API, a server-sent-events stream for live
// data and the embedded browser client.
package server

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"navtex/internal/config"
)

// Role is what a client may do.
type Role int

const (
	Viewer Role = iota // may watch
	Admin              // may also change settings and data
)

func (r Role) String() string { return [...]string{"viewer", "admin"}[r] }

// Identity is the result of classifying a request.
type Identity struct {
	Role      Role
	NeedToken bool   // would be admin but has not presented the access token
	Why       string // for the log / tests: why the role was chosen
	Remote    bool   // an administrator who is not on the local network: got in with the access token (server.remote_admin)
}

// proxyHeaders mark a request that passed through a proxy or tunnel. Such a request looks local (it comes from the
// tunnel program on this computer) but really comes from anywhere, so it is never an administrator.
var proxyHeaders = []string{"Cf-Connecting-Ip", "Cf-Ray", "Cf-Visitor", "Cf-Ipcountry", "Cdn-Loop", "X-Forwarded-For",
	"X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded", "True-Client-Ip", "Via", "X-Original-Forwarded-For"}

// Access decides roles. Settings are read on every request so that changes apply at once.
type Access struct {
	Settings func() config.Server
	Limiter  *loginLimiter // counts wrong tokens presented by remote clients (nil in tests that do not need it)
	hostname string
}

// NewAccess creates an Access reading its rules from settings.
func NewAccess(settings func() config.Server) *Access {
	h, _ := os.Hostname()
	return &Access{Settings: settings, hostname: strings.ToLower(h)}
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Classify decides the role of a request. tunnel is true for requests arriving on the dedicated tunnel listener.
//
// A client that is not on the local network is a viewer, unless server.remote_admin is on and the request carries the access
// token: then it is an administrator ("remote"). Wrong tokens from such clients are counted and, after a few, refused for a
// while, whichever way they arrive (the login box, a header, a cookie), so that the token cannot be guessed.
func (a *Access) Classify(r *http.Request, tunnel bool) Identity {
	id := a.classify(r, tunnel)
	if id.Role != Viewer || id.NeedToken {
		return id
	}
	set := a.Settings()
	if !set.RemoteAdmin || set.Token == "" {
		return id
	}
	got := presentedToken(r)
	if got == "" {
		return id
	}
	key := loginKey(r)
	if a.Limiter != nil {
		if blocked, _ := a.Limiter.blocked(key); blocked {
			return id
		}
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(set.Token)) == 1 {
		return Identity{Admin, false, "remote administrator (access token)", true}
	}
	if a.Limiter != nil {
		a.Limiter.failedToken(key, got)
	}
	return id
}

func (a *Access) classify(r *http.Request, tunnel bool) Identity {
	set := a.Settings()
	if tunnel {
		return Identity{Viewer, false, "tunnel listener", false}
	}
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return Identity{Viewer, false, "proxy header " + h, false}
		}
	}
	host := strings.ToLower(hostOnly(r.Host))
	for _, p := range splitList(set.PublicHosts) {
		if strings.EqualFold(p, host) {
			return Identity{Viewer, false, "public host name", false}
		}
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return Identity{Viewer, false, "unparsable peer address", false}
	}
	ip := ap.Addr().Unmap()
	if !a.trustedNet(ip, set) {
		return Identity{Viewer, false, "remote address", false}
	}
	if !a.knownHost(host, set) {
		return Identity{Viewer, false, "unknown host name (possible DNS rebinding)", false}
	}
	if set.Token != "" && !tokenOK(r, set.Token) {
		return Identity{Viewer, true, "token needed", false}
	}
	return Identity{Admin, false, "local network", false}
}

func (a *Access) trustedNet(ip netip.Addr, set config.Server) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	for _, n := range splitList(set.AdminNetworks) {
		if p, err := netip.ParsePrefix(n); err == nil && p.Contains(ip) {
			return true
		}
		if x, err := netip.ParseAddr(n); err == nil && x == ip {
			return true
		}
	}
	return false
}

// knownHost accepts IP literals, localhost, single-label and .local / .lan names, this computer's name and configured hosts.
func (a *Access) knownHost(host string, set config.Server) bool {
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if host == "localhost" || host == a.hostname || !strings.Contains(host, ".") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".home.arpa") {
		return true
	}
	for _, h := range splitList(set.Hosts) {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(hostport, "[]")
}

// presentedToken is the access token a request carries: the header, else the cookie.
func presentedToken(r *http.Request) string {
	got := r.Header.Get("X-Navtex-Token")
	if got == "" {
		if c, err := r.Cookie("navtex_token"); err == nil {
			got = c.Value
		}
	}
	return got
}

func tokenOK(r *http.Request, token string) bool {
	got := presentedToken(r)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// isHTTPS tells whether the browser reached us over HTTPS: directly, or through a proxy or tunnel that says so.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return strings.Contains(strings.ReplaceAll(r.Header.Get("Cf-Visitor"), " ", ""), `"scheme":"https"`)
}

// CheckWrite validates a state-changing request: it must come from our own page (same origin), which defends against
// other web pages using a browser on the local network to attack the server.
func CheckWrite(r *http.Request) error {
	if r.Header.Get("X-Navtex") != "1" {
		return errors.New("missing X-Navtex header")
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
		return errors.New("cross-site request")
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return errors.New("origin does not match host")
		}
	}
	return nil
}
