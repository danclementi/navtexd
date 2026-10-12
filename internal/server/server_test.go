package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"navtex/internal/app"
	"navtex/internal/catalog"
	"navtex/internal/config"
	"navtex/internal/hpsdr"
	"navtex/internal/store"
)

const recording = "../../recordings/SDRconnect_IQ_20260928_204010_517000HZ_audio48k.wav"

func setup(t *testing.T, cfg config.Config) (*Server, *app.Runner, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st)
	r := app.New(dir, cfg, st, s.Hooks())
	cat, err := catalog.Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	cat.Position = func() (float64, float64, bool) {
		o := r.Config().Operator
		return o.LatDeg, o.LonDeg, o.LatDeg != 0 || o.LonDeg != 0
	}
	r.Enrich = cat.Enrich
	r.Keep = func(x store.Record) bool { return cat.Logged(x.StationID) }
	s.Attach(r, cat)
	t.Cleanup(r.Stop)
	return s, r, st, dir
}

func do(t *testing.T, ts *httptest.Server, method, path string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("X-Navtex", "1")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPIRolesSettingsAndControls(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, r, _, dir := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()

	code, h := do(t, admin, "GET", "/api/state", nil, nil)
	if code != 200 || h["role"] != "admin" {
		t.Fatalf("state: %d %v", code, h)
	}
	if _, h = do(t, tunnel, "GET", "/api/state", nil, nil); h["role"] != "viewer" {
		t.Errorf("tunnel role %v", h["role"])
	}
	if code, _ = do(t, tunnel, "GET", "/api/settings", nil, nil); code != 403 {
		t.Errorf("tunnel settings: %d", code)
	}
	if code, _ = do(t, tunnel, "POST", "/api/control/resync", nil, nil); code != 403 {
		t.Errorf("tunnel control: %d", code)
	}
	if code, _ = do(t, admin, "GET", "/api/settings", nil, map[string]string{"X-Forwarded-For": "9.9.9.9"}); code != 403 {
		t.Errorf("forwarded settings: %d", code)
	}
	if code, _ = do(t, admin, "POST", "/api/control/polarity", map[string]any{"value": "inverted"}, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Errorf("bad origin: %d", code)
	}
	if code, _ = do(t, admin, "POST", "/api/control/polarity", map[string]any{"value": "inverted"}, nil); code != 200 {
		t.Errorf("polarity: %d", code)
	}
	if code, _ = do(t, admin, "POST", "/api/control/polarity", map[string]any{"value": "sideways"}, nil); code != 400 {
		t.Errorf("bad polarity: %d", code)
	}
	if code, _ = do(t, admin, "POST", "/api/control/carrier", map[string]any{"value": 20}, nil); code != 400 {
		t.Errorf("bad carrier: %d", code)
	}
	if code, b := do(t, admin, "PUT", "/api/settings", map[string]any{"changes": map[string]any{"operator.comment": "loop antenna", "decoder.lock_words": 3}}, nil); code != 200 {
		t.Errorf("settings put: %d %v", code, b)
	}
	if code, _ = do(t, admin, "PUT", "/api/settings", map[string]any{"changes": map[string]any{"decoder.lock_words": -1}}, nil); code != 400 {
		t.Errorf("invalid settings accepted: %d", code)
	}
	r.Start()
	time.Sleep(300 * time.Millisecond)
	c := r.Config()
	if c.Operator.Comment != "loop antenna" || c.Decoder.MinRun != 3 || c.Decoder.Polarity.String() != "inverted" {
		t.Errorf("config not applied: %+v", c.Decoder)
	}
	saved, err := config.Load(filepath.Join(dir, app.SettingsFile))
	if err != nil || saved.Operator.Comment != "loop antenna" {
		t.Errorf("settings not saved: %v %v", err, saved.Operator.Comment)
	}
	if st := r.State(); st.Running || st.Error == "" {
		t.Errorf("a missing input file should be reported: %+v", st)
	}
}

func TestViewerNeverGetsPosition(t *testing.T) {
	cfg := config.Default()
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 40.123456, -40.654321
	s, _, _, _ := setup(t, cfg)
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	s.updateSun()
	for _, p := range []string{"/api/state", "/api/messages"} {
		resp, err := http.Get(tunnel.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		resp.Body.Close()
		if strings.Contains(b.String(), "40.12") || strings.Contains(b.String(), "40.65") {
			t.Errorf("%s leaks the position: %s", p, b.String())
		}
	}
	if _, h := do(t, tunnel, "GET", "/api/state", nil, nil); h["sun"] != nil {
		t.Error("viewers must not get sun times by default (they reveal the position)")
	}
	if _, h := do(t, admin, "GET", "/api/state", nil, nil); h["sun"] == nil {
		t.Error("admin sun times missing")
	}
}

// TestLiveRecording runs the recording through the runner and checks the stored records and the event stream.
func TestLiveRecording(t *testing.T) {
	if _, err := os.Stat(recording); err != nil {
		t.Skip("recording not available")
	}
	cfg := config.Default()
	cfg.Input.File.Path = recording
	cfg.Input.Source = "file"
	s, r, st, _ := setup(t, cfg)
	ts := httptest.NewServer(s.handler(false))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	counts := map[string]int{}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if n, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				counts[n]++
				if counts["record"] >= 6 {
					close(done)
					return
				}
			}
		}
	}()
	r.Start()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out; events so far %v", counts)
	}
	if st.Count() != 6 {
		t.Errorf("stored %d records, want 6", st.Count())
	}
	for _, e := range []string{"hello", "meters", "spectrum", "text", "record"} {
		if counts[e] == 0 {
			t.Errorf("no %q events (%v)", e, counts)
		}
	}
}

func TestListenNearWhenBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer busy.Close()
	ln, err := listenNear(busy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().String() == busy.Addr().String() {
		t.Error("got the busy address")
	}
}

func TestRadiosListAndControl(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, r, _, dir := setup(t, cfg)
	r.Radios = hpsdr.OpenMRU(filepath.Join(dir, "radios.json"))
	r.Radios.Touch("10.0.0.1", "R_PITAYA")
	r.Radios.Touch("10.0.0.2", "")
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()

	get := func(ts *httptest.Server) (int, []map[string]any) {
		resp, err := http.Get(ts.URL + "/api/radios")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l []map[string]any
		json.NewDecoder(resp.Body).Decode(&l)
		return resp.StatusCode, l
	}
	code, l := get(admin)
	if code != 200 || len(l) != 2 || l[0]["addr"] != "10.0.0.2" || l[1]["label"] != "10.0.0.1  R_PITAYA" {
		t.Fatalf("radio list: %d %v", code, l)
	}
	if code, _ = get(tunnel); code != 403 {
		t.Fatalf("a viewer must not see the radio addresses: %d", code)
	}
	if _, c := do(t, tunnel, "GET", "/api/hello", nil, nil); c != nil {
		if b, _ := json.Marshal(c); strings.Contains(string(b), "10.0.0.") {
			t.Fatalf("a viewer's page data names a radio: %s", b)
		}
	}
	// choosing a radio from the header sets source and address; an audio device takes the source back
	if code, _ = do(t, admin, "POST", "/api/control/radio", map[string]any{"value": "127.0.0.1"}, nil); code != 200 {
		t.Fatalf("radio control: %d", code)
	}
	if c := r.Config(); c.Input.Source != "hpsdr" || c.Input.HPSDR.Address != "127.0.0.1" {
		t.Fatalf("after choosing a radio: %q %q", c.Input.Source, c.Input.HPSDR.Address)
	}
	if code, _ = do(t, tunnel, "POST", "/api/control/radio", map[string]any{"value": "10.0.0.1"}, nil); code != 403 {
		t.Fatalf("viewer radio control: %d", code)
	}
	if code, _ = do(t, admin, "POST", "/api/control/device", map[string]any{"value": "Line 1"}, nil); code != 200 {
		t.Fatalf("device control: %d", code)
	}
	if c := r.Config(); c.Input.Source != "device" || c.Input.Device.Name != "Line 1" {
		t.Fatalf("after choosing an audio input: %q %q", c.Input.Source, c.Input.Device.Name)
	}
}
