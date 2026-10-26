package spyserver

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a stand-in for a SpyServer with an Airspy HF+ Discovery: it sends the device information and a client sync, takes
// settings, and streams a tone 12 kHz above the I/Q centre once streaming is switched on.
type fake struct {
	mu        sync.Mutex
	ln        net.Listener
	conn      net.Conn
	maxRate   uint32
	canCtl    uint32
	minIQ     uint32
	ignoreF   bool // does not take the I/Q frequency
	skipSeq   int  // leave out this many sequence numbers after the first block
	settings  map[uint32]uint32
	centre    uint32
	streaming bool
	conns     int
}

func newFake(t *testing.T) *fake {
	f := &fake{maxRate: 912000, canCtl: 1, minIQ: 390000, settings: map[uint32]uint32{}, centre: 100000000}
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
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fake) addr() string { return f.ln.Addr().String() }

func (f *fake) msg(c net.Conn, typ uint32, seq uint32, body []byte) {
	b := make([]byte, 20+len(body))
	binary.LittleEndian.PutUint32(b, 0x53505953)
	binary.LittleEndian.PutUint32(b[4:], typ)
	binary.LittleEndian.PutUint32(b[8:], 0)
	binary.LittleEndian.PutUint32(b[12:], seq)
	binary.LittleEndian.PutUint32(b[16:], uint32(len(body)))
	copy(b[20:], body)
	c.Write(b)
}

func u32(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}

func (f *fake) sync(c net.Conn) {
	f.mu.Lock()
	b := u32(f.canCtl, 5, 100000000, f.centre, f.centre, f.minIQ, 1699610000, f.minIQ, 1699610000)
	f.mu.Unlock()
	f.msg(c, 1, 0, b)
}

// moveCentre changes the I/Q centre on the server's own initiative (another client, or the operator).
func (f *fake) moveCentre(v uint32) {
	f.mu.Lock()
	f.centre = v
	c := f.conn
	f.mu.Unlock()
	f.sync(c)
}

func (f *fake) serve(c net.Conn) {
	f.mu.Lock()
	f.conn = c
	f.conns++
	f.streaming = false
	f.mu.Unlock()
	defer c.Close()
	var h [8]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return
	}
	hello := make([]byte, binary.LittleEndian.Uint32(h[4:]))
	io.ReadFull(c, hello)
	f.mu.Lock()
	rate := f.maxRate
	f.mu.Unlock()
	f.msg(c, 0, 0, u32(2, 959590453, rate, 780000, 8, 0, 8, 0, 1700000000, 16, 0, 0))
	f.sync(c)
	for {
		if _, err := io.ReadFull(c, h[:]); err != nil {
			return
		}
		body := make([]byte, binary.LittleEndian.Uint32(h[4:]))
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		if binary.LittleEndian.Uint32(h[:]) != 2 {
			continue
		}
		id, v := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint32(body[4:])
		f.mu.Lock()
		f.settings[id] = v
		ign := f.ignoreF
		if id == 101 && !ign {
			f.centre = v
		}
		on := id == 1 && v == 1
		if id == 1 {
			f.streaming = v == 1
		}
		f.mu.Unlock()
		if on { // like the real server, the new I/Q centre is reported once streaming has started
			f.sync(c)
			go f.stream(c)
		}
	}
}

func (f *fake) stream(c net.Conn) {
	f.mu.Lock()
	stage := f.settings[102]
	rate := float64(f.maxRate) / math.Pow(2, float64(stage))
	skip := f.skipSeq
	f.mu.Unlock()
	const n = 2000
	var seq uint32
	k := 0
	t0 := time.Now()
	for {
		f.mu.Lock()
		on := f.streaming
		f.mu.Unlock()
		if !on {
			return
		}
		b := make([]byte, 4*n)
		for i := 0; i < n; i++ {
			ph := 2 * math.Pi * 12000 * float64(k+i) / rate
			binary.LittleEndian.PutUint16(b[4*i:], uint16(int16(8000*math.Cos(ph))))
			binary.LittleEndian.PutUint16(b[4*i+2:], uint16(int16(8000*math.Sin(ph))))
		}
		k += n
		if seq == 1 && skip > 0 {
			seq += uint32(skip)
		}
		f.msg(c, 101, seq, b)
		seq++
		if d := time.Until(t0.Add(time.Duration(float64(k) / rate * float64(time.Second)))); d > 0 {
			time.Sleep(d)
		}
	}
}

func opts(addr string) Options {
	return Options{Addr: addr, LOHz: 517000, Gain: -1, Timeout: 2 * time.Second}
}

func readN(t *testing.T, s *Source, n int) []complex64 {
	t.Helper()
	out := make([]complex64, 0, n)
	buf := make([]complex64, 4096)
	for len(out) < n {
		k, err := s.Read(buf)
		if err != nil {
			t.Fatalf("read: %v after %d samples", err, len(out))
		}
		out = append(out, buf[:k]...)
	}
	return out
}

func TestStream(t *testing.T) {
	f := newFake(t)
	o := opts(f.addr())
	o.Gain = 3
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.SampleRate() != 228000 || s.CentreHz() != 517000 {
		t.Errorf("rate %v centre %v", s.SampleRate(), s.CentreHz())
	}
	x := readN(t, s, 20000)
	f.mu.Lock()
	st := f.settings
	if st[101] != 517000 || st[102] != 2 || st[100] != 2 || st[2] != 3 || st[0] != 1 {
		t.Errorf("settings %v", st)
	}
	f.mu.Unlock()
	// the tone is 12 kHz above the centre: the phase advances 2*pi*12000/228000 per sample
	var acc complex128
	for i := 1; i < len(x); i++ {
		a, b := complex128(x[i-1]), complex128(x[i])
		acc += b * complex(real(a), -imag(a))
	}
	got := math.Atan2(imag(acc), real(acc)) / (2 * math.Pi) * 228000
	if math.Abs(got-12000) > 50 {
		t.Errorf("tone at %.1f Hz, want 12000", got)
	}
	if !strings.Contains(s.Describe(), "Airspy HF+") {
		t.Errorf("describe: %s", s.Describe())
	}
}

func TestSetupErrors(t *testing.T) {
	f := newFake(t)
	f.canCtl = 0
	if _, err := Open(context.Background(), opts(f.addr())); err == nil || !strings.Contains(err.Error(), "control") {
		t.Errorf("no control: %v", err)
	}
	f = newFake(t)
	f.ignoreF = true
	if _, err := Open(context.Background(), opts(f.addr())); err == nil || !strings.Contains(err.Error(), "did not accept the I/Q centre") {
		t.Errorf("ignored frequency: %v", err)
	}
	f = newFake(t)
	o := opts(f.addr())
	o.LOHz = 100000 // below the 390 kHz this server can tune
	if _, err := Open(context.Background(), o); err == nil || !strings.Contains(err.Error(), "cannot tune") {
		t.Errorf("below minimum: %v", err)
	}
	f = newFake(t)
	o = opts(f.addr())
	o.Gain = 20
	if _, err := Open(context.Background(), o); err == nil || !strings.Contains(err.Error(), "gain index") {
		t.Errorf("gain: %v", err)
	}
	if _, err := Open(context.Background(), Options{Addr: "127.0.0.1:1", LOHz: 517000}); err == nil || !strings.Contains(err.Error(), "SpyServer at") {
		t.Errorf("no server: %v", err)
	}
}

func TestStageChoice(t *testing.T) {
	f := newFake(t)
	f.maxRate = 768000 // HF+ (older firmware): 192 kS/s is nearer 250 kS/s than 384 kS/s
	s, err := Open(context.Background(), opts(f.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.SampleRate() != 192000 {
		t.Errorf("rate %v", s.SampleRate())
	}
}

func TestSequenceGapWarned(t *testing.T) {
	f := newFake(t)
	f.skipSeq = 5
	var mu sync.Mutex
	var warn string
	o := opts(f.addr())
	o.Notice = func(level, text string) {
		if level == "warn" {
			mu.Lock()
			warn = text
			mu.Unlock()
		}
	}
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	readN(t, s, 10000)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(warn, "5 blocks") {
		t.Errorf("warning %q", warn)
	}
}

func TestCentreMovedEndsStream(t *testing.T) {
	f := newFake(t)
	s, err := Open(context.Background(), opts(f.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.moveCentre(800000)
	buf := make([]complex64, 4096)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("stream did not end")
		default:
		}
		if _, err := s.Read(buf); err != nil {
			if !strings.Contains(err.Error(), "I/Q centre frequency was changed") {
				t.Errorf("error: %v", err)
			}
			return
		}
	}
}

func TestServerClosesAndReturnsOnce(t *testing.T) {
	retryEvery = 100 * time.Millisecond
	defer func() { retryEvery = 5 * time.Second }()
	f := newFake(t)
	var mu sync.Mutex
	var notes []string
	o := opts(f.addr())
	o.Notice = func(level, text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }
	s, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	readN(t, s, 1000)
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
	for want := 1; want <= 2; want++ {
		f.mu.Lock()
		f.conn.Close()
		f.mu.Unlock()
		for i := 0; i < 100 && count("is back") < want; i++ {
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(400 * time.Millisecond)
	}
	if count("closed the connection") != 2 || count("is back") != 2 {
		t.Errorf("closed %d, back %d: %v", count("closed the connection"), count("is back"), notes)
	}
	readN(t, s, 5000)
}
