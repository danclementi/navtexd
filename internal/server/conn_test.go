package server

import (
	"net/http/httptest"
	"testing"
)

func TestUASummary(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36":                         "Chrome 129 on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.2792.52":       "Edge 129 on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0":                                                        "Firefox 130 on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1": "Safari 17 on iPhone",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15":                   "Safari 17 on macOS",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36":                   "Chrome 129 on Android",
		"curl/8.5.0": "curl 8",
		"":           "unknown browser",
	} {
		if got := uaSummary(ua); got != want {
			t.Errorf("%q: got %q, want %q", ua, got, want)
		}
	}
}

func TestClientAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.20:5555"
	if ip, p := clientAddr(r); ip != "192.168.1.20" || p {
		t.Errorf("peer: %s %v", ip, p)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Cf-Connecting-Ip", "203.0.113.9")
	if ip, p := clientAddr(r); ip != "203.0.113.9" || !p {
		t.Errorf("cloudflare: %s %v", ip, p)
	}
	r.Header.Del("Cf-Connecting-Ip")
	r.Header.Set("X-Forwarded-For", "198.51.100.4, 10.0.0.1")
	if ip, _ := clientAddr(r); ip != "198.51.100.4" {
		t.Errorf("xff: %s", ip)
	}
	for ip, want := range map[string]bool{"127.0.0.1": true, "192.168.4.5": true, "10.1.2.3": true, "100.80.1.1": true, "::1": true, "203.0.113.9": false, "8.8.8.8": false} {
		if isLocalAddr(ip) != want {
			t.Errorf("isLocalAddr(%s) != %v", ip, want)
		}
	}
	r.Header.Set("Cf-Ipcountry", "DE")
	r.Header.Set("Cf-Ipcity", "Berlin")
	if p, c := headerPlace(r); p != "Berlin, DE" || !c {
		t.Errorf("headerPlace: %q %v", p, c)
	}
}
