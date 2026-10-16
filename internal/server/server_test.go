package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	if st := r.State(0); st.Running || st.Error == "" {
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

// TestSecondChannelOffAndOn: the second channel is off by default, and switching it on from the settings starts it (here
// with an input that does not exist, which it reports as its own error, tagged in the system log).
func TestSecondChannelOffAndOn(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Extra[0].Input.Source = "file"
	cfg.Extra[0].Input.File.Path = "/nonexistent2.wav"
	s, r, st, _ := setup(t, cfg)
	ts := httptest.NewServer(s.handler(false))
	defer ts.Close()
	r.Start()
	code, h := do(t, ts, "GET", "/api/state", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	chans, _ := h["chans"].([]any)
	if len(chans) != config.Channels {
		t.Fatalf("chans: %v", h["chans"])
	}
	c1 := chans[1].(map[string]any)["controls"].(map[string]any)
	if c1["enabled"] != false || c1["label"] != "Channel 2" || c1["channel"] != float64(490) {
		t.Errorf("second channel controls: %v", c1)
	}
	if r.State(1).Error != "" || r.State(1).Running {
		t.Errorf("a disabled channel must not run: %+v", r.State(1))
	}
	hdr := map[string]string{"X-Navtex": "1"}
	code, _ = do(t, ts, "PUT", "/api/settings", map[string]any{"changes": map[string]any{"extra.0.enabled": true, "extra.0.channel.name": "Loop"}}, hdr)
	if code != 200 {
		t.Fatalf("enable second: %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.State(1).Error == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.State(1).Error == "" {
		t.Fatal("the second channel did not start")
	}
	found := false
	for _, e := range st.Syslog(0) {
		if strings.HasPrefix(e.Text, "[Loop] ") && e.Level == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("no tagged error line in the system log: %v", st.Syslog(0))
	}
	if r.State(0).Rcv != 0 || r.State(1).Rcv != 1 {
		t.Error("state does not carry its channel")
	}
	// the first channel's control must not touch the second
	if code, _ = do(t, ts, "POST", "/api/control/polarity?ch=1", map[string]any{"value": "inverted"}, hdr); code != 200 {
		t.Fatalf("polarity ch=1: %d", code)
	}
	if c := r.Config(); c.Extra[0].Decoder.Polarity == c.Decoder.Polarity {
		t.Errorf("polarity went to the wrong channel: %v / %v", c.Decoder.Polarity, c.Extra[0].Decoder.Polarity)
	}
	// a third channel works the same way, and a channel beyond the last is refused
	if code, _ = do(t, ts, "POST", "/api/control/polarity?ch=2", map[string]any{"value": "inverted"}, hdr); code != 200 {
		t.Errorf("ch=2: %d", code)
	}
	if c := r.Config(); c.Extra[1].Decoder.Polarity != c.Extra[0].Decoder.Polarity || c.Extra[1].Decoder.Polarity == c.Decoder.Polarity {
		t.Error("polarity of channel 3 was not set")
	}
	if code, _ = do(t, ts, "POST", "/api/control/polarity?ch="+strconv.Itoa(config.Channels), map[string]any{"value": "inverted"}, hdr); code != 400 {
		t.Errorf("a channel beyond the last must be refused: %d", code)
	}
}

// TestTwoChannelsDecode runs the recording through both channels at once: every record is tagged with its channel, and
// the events of the second channel say so.
func TestTwoChannelsDecode(t *testing.T) {
	if _, err := os.Stat(recording); err != nil {
		t.Skip("recording not available")
	}
	cfg := config.Default()
	cfg.Input.Source, cfg.Input.File.Path = "file", recording
	cfg.Extra[0].Enabled = true
	cfg.Extra[0].Input.Source, cfg.Extra[0].Input.File.Path = "file", recording
	cfg.Extra[0].Channel.FrequencyHz = 518000
	cfg.Extra[0].Channel.Comment = "second loop"
	s, r, st, _ := setup(t, cfg)
	ts := httptest.NewServer(s.handler(false))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var mu sync.Mutex
	sawRcv := map[string]int{}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		ev := ""
		for sc.Scan() {
			if n, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				ev = n
			}
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok && (ev == "meters" || ev == "text" || ev == "spectrum") {
				var m struct {
					Rcv int `json:"rcv"`
				}
				json.Unmarshal([]byte(d), &m)
				mu.Lock()
				sawRcv[fmt.Sprint(ev, m.Rcv)]++
				mu.Unlock()
			}
			if ev == "record" {
				recs, _ := st.Query(store.Filter{})
				n0, n1 := 0, 0
				for _, x := range recs {
					if x.Rcv == 1 {
						n1++
					} else {
						n0++
					}
				}
				if n0 >= 1 && n1 >= 1 {
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
		recs, _ := st.Query(store.Filter{})
		n1 := 0
		for _, x := range recs {
			n1 += x.Rcv
		}
		mu.Lock()
		t.Fatalf("timed out waiting for records from both channels: %d records (%d from the second); events %v; states %+v / %+v", len(recs), n1, sawRcv, r.State(0), r.State(1))
	}
	recs, _ := st.Query(store.Filter{})
	for _, x := range recs {
		if x.Rcv == 1 && !strings.Contains(x.Comment, "second loop") {
			t.Errorf("record %d from the second channel lacks its comment: %q", x.ID, x.Comment)
		}
		if x.Rcv == 0 && strings.Contains(x.Comment, "second loop") {
			t.Errorf("record %d from the first channel has the second channel's comment", x.ID)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, k := range []string{"meters0", "meters1", "text0", "text1", "spectrum0", "spectrum1"} {
		if sawRcv[k] == 0 {
			t.Errorf("no %s events: %v", k, sawRcv)
		}
	}
}
