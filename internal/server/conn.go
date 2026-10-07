package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"navtex/internal/config"
)

// Console messages about browsers connecting and disconnecting: who (address, place, browser) and how long.

// clientAddr returns the address a request really came from: behind a tunnel or proxy the address in its headers, otherwise
// the peer address. proxied says that a proxy header was used (the value is then only as trustworthy as the proxy).
func clientAddr(r *http.Request) (ip string, proxied bool) {
	for _, h := range []string{"Cf-Connecting-Ip", "True-Client-Ip", "X-Real-Ip"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v, true
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0]), true
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, false
	}
	return h, false
}

func isLocalAddr(ip string) bool {
	a, err := netip.ParseAddr(strings.Trim(ip, "[]"))
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() || (a.Is4() && a.As4()[0] == 100 && a.As4()[1]&0xC0 == 64) // 100.64.0.0/10 (CGNAT, Tailscale)
}

// uaSummary names the browser and system in a User-Agent header, e.g. "Chrome 129 on Windows", "Safari 17 on iPhone".
func uaSummary(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return "unknown browser"
	}
	ver := func(tok string) string { // major version after tok
		i := strings.Index(ua, tok)
		if i < 0 {
			return ""
		}
		v := ua[i+len(tok):]
		if j := strings.IndexAny(v, ". ;)"); j >= 0 {
			v = v[:j]
		}
		return v
	}
	name := ""
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "EdgA/") || strings.Contains(ua, "EdgiOS/"):
		name = "Edge " + firstNonEmpty(ver("Edg/"), ver("EdgA/"), ver("EdgiOS/"))
	case strings.Contains(ua, "OPR/"):
		name = "Opera " + ver("OPR/")
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		name = "Firefox " + firstNonEmpty(ver("Firefox/"), ver("FxiOS/"))
	case strings.Contains(ua, "SamsungBrowser/"):
		name = "Samsung Internet " + ver("SamsungBrowser/")
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		name = "Chrome " + firstNonEmpty(ver("Chrome/"), ver("CriOS/"))
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		name = "Safari " + ver("Version/")
	case strings.HasPrefix(ua, "curl/"):
		name = "curl " + ver("curl/")
	default:
		name = ua
		if len(name) > 40 {
			name = name[:40] + "..."
		}
	}
	name = strings.TrimSpace(name)
	osn := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		osn = "iPhone"
	case strings.Contains(ua, "iPad"):
		osn = "iPad"
	case strings.Contains(ua, "Android"):
		osn = "Android"
	case strings.Contains(ua, "Windows"):
		osn = "Windows"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		osn = "macOS"
	case strings.Contains(ua, "CrOS"):
		osn = "ChromeOS"
	case strings.Contains(ua, "Linux") || strings.Contains(ua, "X11"):
		osn = "Linux"
	}
	if osn != "" {
		return name + " on " + osn
	}
	return name
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// placer looks places up for the console messages and remembers them.
type placer struct {
	mu    sync.Mutex
	cache map[string]string
	hc    *http.Client
}

func newPlacer() *placer {
	return &placer{cache: map[string]string{}, hc: &http.Client{Timeout: 3 * time.Second}}
}

// lookup asks ipwho.is for the place of a public address; "" if it cannot be found.
func (g *placer) lookup(ctx context.Context, ip string) string {
	g.mu.Lock()
	v, ok := g.cache[ip]
	g.mu.Unlock()
	if ok {
		return v
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://ipwho.is/"+ip+"?fields=success,city,region,country,country_code", nil)
	if err != nil {
		return ""
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var j struct {
		Success     bool   `json:"success"`
		City        string `json:"city"`
		Region      string `json:"region"`
		Country     string `json:"country"`
		CountryCode string `json:"country_code"`
	}
	if json.NewDecoder(resp.Body).Decode(&j) != nil || !j.Success {
		return ""
	}
	v = placeText(j.City, j.Region, firstNonEmpty(j.Country, j.CountryCode))
	g.mu.Lock()
	g.cache[ip] = v
	g.mu.Unlock()
	return v
}

func placeText(parts ...string) string {
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && (len(out) == 0 || out[len(out)-1] != p) {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// headerPlace is the place Cloudflare put into the request headers (country always, city and region if the visitor
// location headers are switched on for the tunnel).
func headerPlace(r *http.Request) (place string, hasCity bool) {
	city := r.Header.Get("Cf-Ipcity")
	cc := r.Header.Get("Cf-Ipcountry")
	if cc == "XX" || cc == "T1" {
		cc = ""
	}
	return placeText(city, r.Header.Get("Cf-Region"), cc), city != ""
}

// noteConn prints "connected" now (after the place has been found) and returns the function that prints "disconnected".
func (s *Server) noteConn(r *http.Request, id Identity, tunnel bool) func() {
	ip, proxied := clientAddr(r)
	ua := uaSummary(r.UserAgent())
	start := time.Now()
	role := id.Role.String()
	via := ""
	if tunnel || proxied {
		via = ", via tunnel"
	}
	place, hasCity := headerPlace(r)
	var mu sync.Mutex
	done := make(chan struct{})
	label := func() string {
		mu.Lock()
		defer mu.Unlock()
		p := place
		if p == "" && isLocalAddr(ip) {
			p = "local network"
		}
		if p == "" {
			p = "location unknown"
		}
		return fmt.Sprintf("%s (%s) %s [%s%s]", ip, p, ua, role, via)
	}
	go func() {
		defer close(done)
		if !hasCity && !isLocalAddr(ip) && s.run != nil && s.run.Config().Server.GeoLookup {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if p := s.places.lookup(ctx, ip); p != "" {
				mu.Lock()
				place = p
				mu.Unlock()
			}
		}
		s.notify(fmt.Sprintf("browser connected:    %s   users %d", label(), s.hub.Clients()))
	}()
	return func() {
		<-done // the connect line comes first
		d := time.Since(start).Round(time.Second)
		s.notify(fmt.Sprintf("browser disconnected: %s   after %v, users %d", label(), d, s.hub.Clients()))
	}
}

func (s *Server) notify(msg string) {
	line := time.Now().UTC().Format(config.TimeFormat) + "  " + msg
	if s.Notify != nil {
		s.Notify(line)
		return
	}
	fmt.Println(line)
}
