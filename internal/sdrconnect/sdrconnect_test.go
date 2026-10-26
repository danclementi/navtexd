package sdrconnect

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"math/cmplx"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"navtex/internal/ws"
)

// fake is a stand-in for SDRconnect's WebSocket server: it keeps properties, answers get/set, selects a device when
// given the right index (base 0 or 1) and, once the I/Q stream is enabled, sends a tone 10 kHz above the centre.
type fake struct {
	mu                 sync.Mutex
	props              map[string]string
	base               int
	log                []string
	iq                 bool
	refuseRate         bool
	controlAfterSelect bool   // can_control becomes true only once a receiver is selected (headless SDRconnect)
	lockCentre         string // if set, device_center_frequency cannot be changed
	refreshing         int    // the next n answers about the receiver lists are "Refreshing..." (SDRconnect just started)
	recentre           bool   // the centre follows the VFO when that leaves the span (SDRconnect 1.0.10, 250 kS/s)
	conn               net.Conn
	ln                 net.Listener
}

func newFake(t *testing.T, base int) *fake {
	f := &fake{base: base, props: map[string]string{
		"api_version": "1.0.3", "can_control": "true", "valid_devices": "RSPduo 1234", "active_device": "", "started": "false",
		"device_sample_rate": "2000000", "device_center_frequency": "100000000", "device_vfo_frequency": "100000000",
		"valid_antennas": "Tuner 1 50 ohm,Tuner 2 50 ohm,HiZ Port", "active_antenna": "Tuner 1 50 ohm", "lna_state": "0", "lna_state_min": "0", "lna_state_max": "9"}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		if f.conn != nil {
			f.conn.Close()
		}
		f.mu.Unlock()
	})
	go f.accept()
	return f
}

func (f *fake) addr() string { return f.ln.Addr().String() }

func (f *fake) accept() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.serve(c)
	}
}

func (f *fake) serve(c net.Conn) {
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+ws.AcceptKey(req.Header.Get("Sec-WebSocket-Key"))+"\r\n\r\n")
	for {
		var h [2]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return
		}
		n := int(h[1] & 0x7f)
		if n == 126 {
			var b [2]byte
			io.ReadFull(br, b[:])
			n = int(binary.BigEndian.Uint16(b[:]))
		}
		var mask [4]byte
		io.ReadFull(br, mask[:])
		p := make([]byte, n)
		io.ReadFull(br, p)
		for i := range p {
			p[i] ^= mask[i&3]
		}
		if h[0]&0x0f == 1 {
			f.handle(p)
		}
	}
}

func (f *fake) send(op byte, p []byte) {
	h := []byte{0x80 | op}
	switch n := len(p); {
	case n < 126:
		h = append(h, byte(n))
	case n < 65536:
		h = append(h, 126)
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conn.Write(append(h, p...))
}

func (f *fake) sendJSON(event, prop, val string) {
	b, _ := json.Marshal(map[string]string{"event_type": event, "property": prop, "value": val})
	f.send(1, b)
}

func (f *fake) handle(p []byte) {
	var m struct{ EventType, Property, Value string }
	var raw map[string]any
	json.Unmarshal(p, &raw)
	m.EventType, _ = raw["event_type"].(string)
	m.Property, _ = raw["property"].(string)
	m.Value, _ = raw["value"].(string)
	f.mu.Lock()
	f.log = append(f.log, m.EventType+" "+m.Property+"="+m.Value)
	f.mu.Unlock()
	switch m.EventType {
	case "get_property":
		f.mu.Lock()
		v := f.props[m.Property]
		if (m.Property == "valid_devices" || m.Property == "active_device") && f.refreshing > 0 {
			v = "Refreshing..."
			if m.Property == "active_device" {
				f.refreshing--
			}
		}
		f.mu.Unlock()
		f.sendJSON("get_property_response", m.Property, v)
	case "set_property":
		if m.Property == "device_sample_rate" && f.refuseRate {
			return
		}
		if m.Property == "device_center_frequency" && f.lockCentre != "" {
			return
		}
		if m.Property == "device_vfo_frequency" && f.recentre {
			v, _ := strconv.ParseFloat(m.Value, 64)
			f.mu.Lock()
			c, _ := strconv.ParseFloat(f.props["device_center_frequency"], 64)
			r, _ := strconv.ParseFloat(f.props["device_sample_rate"], 64)
			f.mu.Unlock()
			if math.Abs(v-c) > r/2 {
				go func() {
					f.mu.Lock()
					f.props["device_center_frequency"] = m.Value
					f.mu.Unlock()
					f.sendJSON("property_changed", "device_center_frequency", m.Value)
				}()
			}
		}
		go func() {
			if m.Property == "device_sample_rate" {
				time.Sleep(300 * time.Millisecond) // a new rate takes a moment to apply
			}
			f.mu.Lock()
			f.props[m.Property] = m.Value
			if m.Property == "device_stream_enable" {
				f.props["started"] = m.Value
			}
			f.mu.Unlock()
			f.sendJSON("property_changed", m.Property, m.Value)
		}()
	case "selected_device":
		if k, _ := strconv.Atoi(m.Value); k == f.base {
			f.mu.Lock()
			f.props["active_device"] = "RSPduo 1234"
			if f.controlAfterSelect {
				f.props["can_control"] = "true"
			}
			f.mu.Unlock()
			f.sendJSON("property_changed", "active_device", "RSPduo 1234")
		}
	case "iq_stream_enable":
		if m.Value == "true" && !f.iq {
			f.iq = true
			go f.stream()
		}
	}
}

// stream sends a tone at +10 kHz from the centre, in frames of 20 ms, with a spectrum frame (type 3) in between.
func (f *fake) stream() {
	f.mu.Lock()
	rate, _ := strconv.ParseFloat(f.props["device_sample_rate"], 64)
	f.mu.Unlock()
	n := int(rate / 50)
	pos := 0
	for i := 0; i < 400; i++ {
		b := make([]byte, 2+4*n)
		binary.LittleEndian.PutUint16(b, 2)
		for k := 0; k < n; k++ {
			ph := 2 * math.Pi * 10000 * float64(pos+k) / rate
			binary.LittleEndian.PutUint16(b[2+4*k:], uint16(int16(16384*math.Cos(ph))))
			binary.LittleEndian.PutUint16(b[4+4*k:], uint16(int16(16384*math.Sin(ph))))
		}
		pos += n
		f.send(2, b)
		f.send(2, []byte{3, 0, 1, 2, 3})
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fake) logged() string { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(f.log, "\n") }

func opts(addr string) Options {
	return Options{Addr: addr, RateHz: 2000000, LOHz: 515000, DialHz: 518000, LNAState: -1}
}

func readN(t *testing.T, s *Source, n int) []complex64 {
	out := make([]complex64, 0, n)
	buf := make([]complex64, 4096)
	for len(out) < n {
		k, err := s.Read(buf)
		if err != nil {
			t.Fatalf("read: %v after %d samples", err, len(out))
		}
		out = append(out, buf[:k]...)
	}
	return out[:n]
}

func TestStreamAndSetup(t *testing.T) {
	for _, base := range []int{0, 1} { // the device index base is not documented
		f := newFake(t, base)
		o := opts(f.addr())
		o.Device = "rspduo"
		o.Antenna = "Hi-Z"
		o.LNAState = 3
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		s, err := Open(ctx, o)
		if err != nil {
			t.Fatalf("base %d: %v", base, err)
		}
		if s.SampleRate() != 250000 || s.CentreHz() != 515000 || s.Describe() == "" {
			t.Errorf("rate %v centre %v", s.SampleRate(), s.CentreHz())
		}
		x := readN(t, s, 20000)
		// the tone is 10 kHz above the centre: 0.2513 rad per sample at 250 kHz; skip the filter's start-up
		var acc complex128
		for i := 5000; i < len(x)-1; i++ {
			acc += complex128(x[i+1]) * cmplx.Conj(complex128(x[i]))
		}
		if w := cmplx.Phase(acc); math.Abs(w-2*math.Pi*10000/250000) > 0.002 {
			t.Errorf("tone phase step %.4f, want %.4f", w, 2*math.Pi*10000/250000)
		}
		if a := cmplx.Abs(complex128(x[10000])); math.Abs(a-0.5) > 0.02 {
			t.Errorf("amplitude %.3f, want 0.5", a)
		}
		l := f.logged()
		for _, want := range []string{"set_property device_sample_rate=2000000", "set_property device_center_frequency=515000", "set_property device_vfo_frequency=518000",
			"set_property active_antenna=HiZ Port", "set_property lna_state=3", "set_property device_stream_enable=true", "iq_stream_enable =true"} {
			if !strings.Contains(l, want) {
				t.Errorf("base %d: not sent: %q\n%s", base, want, l)
			}
		}
		if strings.Index(l, "device_stream_enable") > strings.Index(l, "iq_stream_enable =true") {
			t.Error("the I/Q stream must be enabled last")
		}
		s.Close()
		time.Sleep(100 * time.Millisecond)
		if !strings.Contains(f.logged(), "iq_stream_enable =false") || !strings.Contains(f.logged(), "device_stream_enable=false") {
			t.Errorf("Close did not stop the streams:\n%s", f.logged())
		}
		cancel()
	}
}

func TestSetupErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, 0)
	o := opts(f.addr())
	o.Antenna = "Nope"
	if _, err := Open(ctx, o); err == nil || !strings.Contains(err.Error(), "HiZ Port") {
		t.Errorf("antenna: %v", err)
	}
	f = newFake(t, 0)
	o = opts(f.addr())
	o.Antenna = "Tuner" // fits two
	if _, err := Open(ctx, o); err == nil || !strings.Contains(err.Error(), "fits several") {
		t.Errorf("ambiguous antenna: %v", err)
	}
	f = newFake(t, 0)
	o = opts(f.addr())
	o.LNAState = 12
	if _, err := Open(ctx, o); err == nil || !strings.Contains(err.Error(), "outside the range") {
		t.Errorf("lna: %v", err)
	}
	f = newFake(t, 0)
	f.props["can_control"] = "false"
	var warned string
	o = opts(f.addr())
	o.Notice = func(level, text string) { warned = text }
	if s, err := Open(ctx, o); err != nil || !strings.Contains(warned, "can_control") { // goes on, with a warning
		t.Errorf("control: %v %q", err, warned)
	} else {
		s.Close()
	}
	// headless: a receiver is reported active but is not open: can_control is false until it is selected
	for _, base := range []int{0, 1} {
		f = newFake(t, base)
		f.props["can_control"], f.props["active_device"], f.controlAfterSelect = "false", "RSPduo 1234", true
		o = opts(f.addr())
		o.Device = "rspduo"
		if s, err := Open(ctx, o); err != nil {
			t.Errorf("headless (base %d): %v", base, err)
		} else {
			s.Close()
		}
	}
	f = newFake(t, 0)
	o = opts(f.addr())
	o.Device = "sdrplay-xyz"
	if _, err := Open(ctx, o); err == nil || !strings.Contains(err.Error(), "no receiver matching") {
		t.Errorf("device: %v", err)
	}
	o = opts("127.0.0.1:1")
	if _, err := Open(ctx, o); err == nil || !strings.Contains(err.Error(), "WebSocket server") {
		t.Errorf("refused: %v", err)
	}
	o.RateHz = 3000000
	if _, err := Open(ctx, o); err == nil {
		t.Error("a bad rate was accepted")
	}
}

func TestServerClosesAndReturns(t *testing.T) {
	retryEvery = 100 * time.Millisecond
	defer func() { retryEvery = 5 * time.Second }()
	f := newFake(t, 0)
	var mu sync.Mutex
	var notes []string
	o := opts(f.addr())
	o.Notice = func(level, text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	readN(t, s, 100)
	f.mu.Lock()
	f.conn.Close()
	f.mu.Unlock()
	var all string
	for i := 0; i < 100 && !strings.Contains(all, "is back"); i++ {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		all = strings.Join(notes, "\n")
		mu.Unlock()
	}
	readN(t, s, 20000) // samples flow again after the reconnect
	if !strings.Contains(all, "closed the connection") || !strings.Contains(all, "is back") {
		t.Errorf("notices: %s", all)
	}
}

func TestCentreMovedEndsStream(t *testing.T) {
	f := newFake(t, 0)
	s, err := Open(context.Background(), opts(f.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.sendJSON("property_changed", "device_center_frequency", "800000")
	buf := make([]complex64, 4096)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("stream did not end")
		default:
		}
		if _, err := s.Read(buf); err != nil {
			if !strings.Contains(err.Error(), "centre frequency (LO) was changed") {
				t.Errorf("error: %v", err)
			}
			return
		}
	}
}

func TestRadioStoppedNotice(t *testing.T) {
	f := newFake(t, 0)
	var mu sync.Mutex
	var notes []string
	o := opts(f.addr())
	o.Notice = func(level, text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.sendJSON("property_changed", "started", "true")
	f.sendJSON("property_changed", "started", "false")
	f.sendJSON("property_changed", "started", "true")
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	all := strings.Join(notes, "\n")
	mu.Unlock()
	if !strings.Contains(all, "radio was stopped") || !strings.Contains(all, "running again") {
		t.Errorf("notices: %s", all)
	}
}

func TestForeignRateChangeEndsStream(t *testing.T) {
	f := newFake(t, 0)
	s, err := Open(context.Background(), opts(f.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.sendJSON("property_changed", "device_sample_rate", "6000000")
	buf := make([]complex64, 4096)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("stream did not end")
		default:
		}
		if _, err := s.Read(buf); err != nil {
			if !strings.Contains(err.Error(), "sample rate was changed") {
				t.Errorf("error: %v", err)
			}
			return
		}
	}
}

func TestMatchName(t *testing.T) {
	l := []string{"Tuner 1 50 ohm", "Tuner 2 50 ohm", "HiZ Port"}
	for want, exp := range map[string]string{"Tuner 1": "Tuner 1 50 ohm", "tuner2": "Tuner 2 50 ohm", "hi-z": "HiZ Port", "HiZ Port": "HiZ Port"} {
		if got, err := matchName(l, want); err != nil || got != exp {
			t.Errorf("%q: %q %v", want, got, err)
		}
	}
}

// SDRconnect that keeps its centre frequency: the stream goes on with the centre it has, with a warning.
func TestCentreStaysPut(t *testing.T) {
	f := newFake(t, 0)
	f.lockCentre = "517000"
	f.props["device_center_frequency"] = "517000"
	var warn string
	o := opts(f.addr())
	o.Notice = func(level, text string) { warn = text }
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.CentreHz() != 517000 || !strings.Contains(warn, "would not move its centre") {
		t.Errorf("centre %v warn %q", s.CentreHz(), warn)
	}
	readN(t, s, 1000)
	// far from the channel: an error
	f = newFake(t, 0)
	f.lockCentre = "100000000"
	f.props["device_center_frequency"] = "100000000"
	if _, err := Open(context.Background(), opts(f.addr())); err == nil || !strings.Contains(err.Error(), "did not accept device_center_frequency") {
		t.Errorf("far centre: %v", err)
	}
}

func TestRecentreThroughVFO(t *testing.T) {
	f := newFake(t, 0)
	f.lockCentre = "800000"
	f.recentre = true
	f.props["device_center_frequency"] = "800000"
	f.props["device_vfo_frequency"] = "517000"
	o := opts(f.addr())
	o.RateHz = 250000
	o.LOHz, o.DialHz = 515000, 518000
	var warn string
	o.Notice = func(level, text string) {
		if level == "warn" {
			warn = text
		}
	}
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.CentreHz() != 515000 || warn != "" {
		t.Errorf("centre %v warn %q", s.CentreHz(), warn)
	}
	readN(t, s, 1000)
}

func TestReconnectAfterRefreshingRunsOnce(t *testing.T) {
	retryEvery = 100 * time.Millisecond
	defer func() { retryEvery = 5 * time.Second }()
	f := newFake(t, 0)
	var mu sync.Mutex
	var notes []string
	o := opts(f.addr())
	o.Device = "RSPduo"
	o.Notice = func(level, text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	count := func(sub string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, x := range notes {
			if strings.Contains(x, sub) {
				n++
			}
		}
		return n
	}
	drop := func(want int) {
		f.mu.Lock()
		f.refreshing = 1 // the first attempt finds SDRconnect still starting up and gives up
		f.conn.Close()
		f.mu.Unlock()
		for i := 0; i < 100 && count("is back") < want; i++ {
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(600 * time.Millisecond) // a second reconnect loop would show up now
	}
	drop(1)
	drop(2)
	if count("closed the connection") != 2 || count("is back") != 2 {
		t.Errorf("closed %d, back %d (want 2 and 2): %v", count("closed the connection"), count("is back"), notes)
	}
	readN(t, s, 5000)
}
