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
}

// proxyHeaders mark a request that passed through a proxy or tunnel. Such a request looks local (it comes from the
// tunnel program on this computer) but really comes from anywhere, so it is never an administrator.
var proxyHeaders = []string{"Cf-Connecting-Ip", "Cf-Ray", "Cf-Visitor", "Cf-Ipcountry", "Cdn-Loop", "X-Forwarded-For",
	"X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded", "True-Client-Ip", "Via", "X-Original-Forwarded-For"}

// Access decides roles. Settings are read on every request so that changes apply at once.
type Access struct {
	Settings func() config.Server
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
func (a *Access) Classify(r *http.Request, tunnel bool) Identity {
	set := a.Settings()
	if tunnel {
		return Identity{Viewer, false, "tunnel listener"}
	}
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return Identity{Viewer, false, "proxy header " + h}
		}
	}
	host := strings.ToLower(hostOnly(r.Host))
	for _, p := range splitList(set.PublicHosts) {
		if strings.EqualFold(p, host) {
			return Identity{Viewer, false, "public host name"}
		}
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return Identity{Viewer, false, "unparsable peer address"}
	}
	ip := ap.Addr().Unmap()
	if !a.trustedNet(ip, set) {
		return Identity{Viewer, false, "remote address"}
	}
	if !a.knownHost(host, set) {
		return Identity{Viewer, false, "unknown host name (possible DNS rebinding)"}
	}
	if set.Token != "" && !tokenOK(r, set.Token) {
		return Identity{Viewer, true, "token needed"}
	}
	return Identity{Admin, false, "local network"}
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

func tokenOK(r *http.Request, token string) bool {
	got := r.Header.Get("X-Navtex-Token")
	if got == "" {
		if c, err := r.Cookie("navtex_token"); err == nil {
			got = c.Value
		}
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
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
