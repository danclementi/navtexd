package hpsdr

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRadio behaves like Pavel Demin's sdr-receiver-hpsdr server: it answers discovery, parses the command frames,
// and after a start packet streams a complex tone to the sender at the rate that was set.
type fakeRadio struct {
	conn     *net.UDPConn
	mu       sync.Mutex
	freq     uint32
	rateCode int
	mox      bool // true if any command frame ever had C0 bit 0 set
	started  bool
	stopped  bool
	cmds     int
	drop     map[uint32]bool // data packet sequence numbers not to send
	toneHz   float64
	busy     bool
	done     chan struct{}
}

func newFake(t *testing.T) *fakeRadio {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRadio{conn: c, drop: map[uint32]bool{}, toneHz: 1500, done: make(chan struct{})}
	go f.serve()
	t.Cleanup(func() { close(f.done); c.Close() })
	return f
}

func (f *fakeRadio) port() int { return f.conn.LocalAddr().(*net.UDPAddr).Port }

func (f *fakeRadio) serve() {
	buf := make([]byte, 2048)
	for {
		n, from, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 4 || buf[0] != 0xef || buf[1] != 0xfe {
			continue
		}
		switch {
		case buf[2] == 0x02:
			r := make([]byte, 60)
			copy(r, []byte{0xef, 0xfe, 2, 1, 2, 3, 4, 5, 6, 25, 1, 'F', 'A', 'K', 'E'})
			f.mu.Lock()
			if f.busy {
				r[2] = 3
			}
			f.mu.Unlock()
			f.conn.WriteToUDP(r, from)
		case buf[2] == 0x01 && buf[3] == 0x02 && n == packetLen:
			f.mu.Lock()
			f.cmds++
			for _, off := range []int{11, 523} {
				c0 := buf[off]
				if c0&1 != 0 {
					f.mox = true
				}
				switch c0 {
				case 0:
					f.rateCode = int(buf[off+1] & 3)
				case 4:
					f.freq = binary.BigEndian.Uint32(buf[off+1:])
				}
			}
			f.mu.Unlock()
		case buf[2] == 0x04:
			f.mu.Lock()
			on := buf[3] != 0
			if on && !f.started {
				f.started = true
				go f.stream(from)
			}
			if !on {
				f.stopped = true
			}
			f.mu.Unlock()
		}
	}
}

func (f *fakeRadio) stream(to *net.UDPAddr) {
	f.mu.Lock()
	rate := 48000 << uint(f.rateCode)
	f.mu.Unlock()
	var seq uint32
	phase := 0.0
	step := 2 * math.Pi * f.toneHz / float64(rate)
	perPacket := 126
	period := time.Duration(float64(perPacket) / float64(rate) * float64(time.Second))
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-tick.C:
		}
		f.mu.Lock()
		if f.stopped {
			f.mu.Unlock()
			return
		}
		f.mu.Unlock()
		p := make([]byte, packetLen)
		p[0], p[1], p[2], p[3] = 0xef, 0xfe, 1, 6
		binary.BigEndian.PutUint32(p[4:], seq)
		for fr := 0; fr < 2; fr++ {
			b := p[8+fr*frameLen:]
			b[0], b[1], b[2] = 0x7f, 0x7f, 0x7f
			for k := 0; k < 63; k++ {
				i := int32(math.Cos(phase) * 4000000)
				q := int32(math.Sin(phase) * 4000000)
				phase += step
				g := b[8+k*8:]
				g[0], g[1], g[2] = byte(i>>16), byte(i>>8), byte(i)
				g[3], g[4], g[5] = byte(q>>16), byte(q>>8), byte(q)
			}
		}
		f.mu.Lock()
		dropped := f.drop[seq]
		f.mu.Unlock()
		if !dropped {
			f.conn.WriteToUDP(p, to)
		}
		seq++
	}
}

func readN(t *testing.T, s *Source, n int) []complex64 {
	t.Helper()
	out := make([]complex64, 0, n)
	buf := make([]complex64, 1024)
	dl := time.After(10 * time.Second)
	for len(out) < n {
		done := make(chan struct{})
		var m int
		var err error
		go func() { m, err = s.Read(buf); close(done) }()
		select {
		case <-done:
		case <-dl:
			t.Fatalf("timeout after %d samples", len(out))
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, buf[:m]...)
	}
	return out
}

func TestStreamTuningAndReceiveOnly(t *testing.T) {
	f := newFake(t)
	s, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 96000, FreqHz: 515000, SendGap: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.SampleRate() != 96000 || s.CentreHz() != 515000 {
		t.Fatalf("rate/centre %v %v", s.SampleRate(), s.CentreHz())
	}
	x := readN(t, s, 20000)
	// the fake plays 1500 Hz at the rate the host asked for; measure it from the phase advance
	var sum complex128
	for i := 1; i < len(x); i++ {
		a, b := complex128(x[i-1]), complex128(x[i])
		sum += b * complex(real(a), -imag(a))
	}
	hz := math.Atan2(imag(sum), real(sum)) / (2 * math.Pi) * 96000
	if math.Abs(hz-1500) > 5 {
		t.Fatalf("tone at %.1f Hz, want 1500", hz)
	}
	if mag := math.Hypot(float64(real(x[100])), float64(imag(x[100]))); math.Abs(mag-4000000.0/8388608) > 0.01 {
		t.Fatalf("level %.3f", mag)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.freq != 515000 || f.rateCode != 1 {
		t.Fatalf("radio was set to %d Hz, rate code %d", f.freq, f.rateCode)
	}
	if f.mox {
		t.Fatal("a command frame had the transmit (MOX) bit set")
	}
	if f.cmds < 3 {
		t.Fatalf("only %d command packets", f.cmds)
	}
}

func TestLostPacketsAreFilled(t *testing.T) {
	f := newFake(t)
	f.mu.Lock()
	f.drop[40], f.drop[41], f.drop[300] = true, true, true
	f.mu.Unlock()
	notes := make(chan string, 8)
	s, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 48000, FreqHz: 515000, Notices: notes, Quiet: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	readN(t, s, 126*320)
	st := s.Stats()
	if st.Lost != 3 {
		t.Fatalf("lost %d, want 3", st.Lost)
	}
	// the two gaps are 0.7 s apart, so with a quiet time of 0.3 s each is reported when it is over
	var got []string
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case m := <-notes:
			got = append(got, m)
		case <-deadline:
			t.Fatalf("notices %q, want one for each of the two bursts", got)
		}
	}
	if !strings.Contains(got[0], "2 packet(s) lost in 1 gap(s) between") || !strings.Contains(got[1], "1 packet(s) lost in 1 gap(s) between") || !strings.Contains(got[1], "UTC") {
		t.Fatalf("wrong notices: %q", got)
	}
}

func TestCloseStopsRadio(t *testing.T) {
	f := newFake(t)
	s, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 48000, FreqHz: 515000})
	if err != nil {
		t.Fatal(err)
	}
	readN(t, s, 500)
	s.Close()
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stopped {
		t.Fatal("radio was not told to stop")
	}
	if _, err := s.Read(make([]complex64, 8)); err == nil {
		t.Fatal("Read after Close should fail")
	}
}

func TestBusyAndMissingRadio(t *testing.T) {
	f := newFake(t)
	f.mu.Lock()
	f.busy = true
	f.mu.Unlock()
	if _, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 48000, FreqHz: 1e6}); err == nil {
		t.Fatal("a radio that is streaming to someone else should be refused")
	}
	if _, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: 9, RateHz: 48000, FreqHz: 1e6, Timeout: 200 * time.Millisecond}); err == nil {
		t.Fatal("no radio at the address should be an error")
	}
	if _, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 44100, FreqHz: 1e6}); err == nil {
		t.Fatal("unsupported sample rate")
	}
}

func TestDiscoveryReply(t *testing.T) {
	b := make([]byte, 60)
	copy(b, []byte{0xef, 0xfe, 3, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 25, 1, 'R', '_', 'P', 'I', 'T', 'A', 'Y', 'A', 8})
	r, ok := ParseDiscoveryReply(b, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5)})
	if !ok || !r.Busy || r.Firmware != 25 || r.Board != 1 || r.Name != "R_PITAYA" {
		t.Fatalf("%+v ok=%v", r, ok)
	}
	if _, ok := ParseDiscoveryReply([]byte{1, 2, 3}, nil); ok {
		t.Fatal("garbage accepted")
	}
}

func TestCommandPacketNeverKeys(t *testing.T) {
	for _, rate := range []int{48000, 96000, 192000, 384000} {
		for _, f := range []float64{0, 424e3, 518e3, 30e6} {
			p, err := CommandPacket(7, rate, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, off := range []int{11, 523} {
				if p[off]&1 != 0 {
					t.Fatalf("MOX bit set (rate %d, %v Hz)", rate, f)
				}
			}
			for _, off := range []int{16, 528} { // transmit data areas stay zero
				for _, v := range p[off : off+frameData] {
					if v != 0 {
						t.Fatal("non-zero transmit data")
					}
				}
			}
			if binary.BigEndian.Uint32(p[524:]) != uint32(f+0.5) {
				t.Fatal("frequency not in C1..C4")
			}
		}
	}
}

func TestBusyClearsAfterRestart(t *testing.T) {
	f := newFake(t)
	f.mu.Lock()
	f.busy = true
	f.mu.Unlock()
	go func() {
		time.Sleep(700 * time.Millisecond)
		f.mu.Lock()
		f.busy = false
		f.mu.Unlock()
	}()
	s, err := Open(context.Background(), Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 96000, FreqHz: 515000, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("a radio that frees up within a moment should be used: %v", err)
	}
	s.Close()
}

// Cancelling the context and then closing (what a channel restart does) must never hang.
func TestCancelThenClose(t *testing.T) {
	f := newFake(t)
	for i := 0; i < 40; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := Open(ctx, Options{Addr: "127.0.0.1", Port: f.port(), RateHz: 96000, FreqHz: 515000, SendGap: 5 * time.Millisecond, Timeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		done := make(chan struct{})
		go func() { s.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("Close hung after the context was cancelled (round %d)", i)
		}
		f.mu.Lock()
		f.started, f.stopped = false, false
		f.mu.Unlock()
	}
}

// A host that answers with something that is not a discovery reply, or not at all, is described in the error.
func TestDiscoverNote(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	port := srv.LocalAddr().(*net.UDPAddr).Port
	got := make(chan int, 4)
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := srv.ReadFromUDP(b)
			if err != nil {
				return
			}
			got <- n
			srv.WriteToUDP([]byte("hello"), from)
		}
	}()
	found, note, err := discover(context.Background(), "127.0.0.1", port, 300*time.Millisecond)
	if err != nil || len(found) != 0 || !strings.Contains(note, "not with an HPSDR discovery reply") {
		t.Fatalf("found=%v note=%q err=%v", found, note, err)
	}
	if n := <-got; n != 63 {
		t.Fatalf("discovery request is %d bytes, want 63", n)
	}
	srv.Close()
	_, note, _ = discover(context.Background(), "127.0.0.1", port, 300*time.Millisecond)
	if note == "" {
		t.Fatal("no note for a silent host")
	}
}
