package hpsdr

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"sync"
	"testing"
	"time"
)

func TestP2PacketLayout(t *testing.T) {
	g := p2General(7)
	if len(g) != 60 || binary.BigEndian.Uint32(g) != 7 || g[4] != 0 || g[37] != 0x08 || g[58] != 0 || g[59] != 0 {
		t.Errorf("general packet: % x", g)
	}
	var on [MaxReceivers]bool
	on[0], on[2] = true, true
	rx := p2RxSpecific(1, 48, on)
	if len(rx) != 1444 || rx[7] != 0b101 || binary.BigEndian.Uint16(rx[18:]) != 48 || rx[22] != 24 || binary.BigEndian.Uint16(rx[18+12:]) != 48 || rx[22+12] != 24 || rx[22+6] != 0 {
		t.Errorf("receiver packet: % x", rx[:40])
	}
	var hz [MaxReceivers]float64
	hz[0], hz[2] = 518000+3000, 14100000
	hi := p2High(2, true, hz, on)
	if len(hi) != 1444 || hi[4] != 1 {
		t.Fatalf("high-priority packet: run byte %x", hi[4])
	}
	if got, want := binary.BigEndian.Uint32(hi[9:]), uint32(math.Round(521000*4294967296.0/122880000.0)); got != want {
		t.Errorf("phase word of 521 kHz = %d, want %d", got, want)
	}
	if binary.BigEndian.Uint32(hi[9+4:]) != 0 { // a receiver that is off has no frequency
		t.Errorf("receiver 2 should be off")
	}
	// nothing that transmits: MOX (bit 1 of byte 4), the transmit frequency, the drive level and the Alex words are zero
	if hi[4]&2 != 0 || binary.BigEndian.Uint32(hi[329:]) != 0 || hi[345] != 0 || binary.BigEndian.Uint32(hi[1428:]) != 0 || binary.BigEndian.Uint32(hi[1432:]) != 0 {
		t.Errorf("high-priority packet sets something that transmits")
	}
	stop := p2High(3, false, [MaxReceivers]float64{}, [MaxReceivers]bool{})
	if stop[4] != 0 {
		t.Errorf("stop packet still runs")
	}
}

func TestP2DiscoveryReply(t *testing.T) {
	b := make([]byte, 60)
	b[4] = 2
	copy(b[5:], []byte{1, 2, 3, 4, 5, 6})
	b[11], b[13] = 5, 21
	r, ok := ParseDiscoveryReply2(b, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 9), Port: 1024})
	if !ok || r.Board != 5 || r.Firmware != 21 || r.Protocol != 2 || r.Busy || r.Name != "Orion Mk II" || r.MAC.String() != "01:02:03:04:05:06" {
		t.Errorf("reply: %+v ok=%v", r, ok)
	}
	b[4] = 3
	if r, _ := ParseDiscoveryReply2(b, nil); !r.Busy {
		t.Errorf("status 3 is busy")
	}
	if _, ok := ParseDiscoveryReply2(make([]byte, 1444), nil); ok { // a data packet
		t.Errorf("a 1444-byte packet is not a discovery reply")
	}
	// a protocol 1 reply is not a protocol 2 reply, and the other way round
	if _, ok := ParseDiscoveryReply2([]byte{0xef, 0xfe, 2, 1, 2, 3, 4, 5, 6, 7, 1, 0, 0, 0}, nil); ok {
		t.Errorf("a protocol 1 reply was taken for protocol 2")
	}
	if _, ok := ParseDiscoveryReply(b, nil); ok {
		t.Errorf("a protocol 2 reply was taken for protocol 1")
	}
}

// fakeP2 is a protocol 2 radio on loopback: it answers discovery, notes what the host sets, and when run is set sends
// each enabled receiver's stream from its own port, a tone whose frequency is that of the receiver (in the test: the
// receiver number).
type fakeP2 struct {
	t       *testing.T
	base    int
	gen     *net.UDPConn
	rx, hi  *net.UDPConn
	data    [MaxReceivers]*net.UDPConn
	mu      sync.Mutex
	host    *net.UDPAddr
	run     bool
	on      [MaxReceivers]bool
	rateKHz int
	freq    [MaxReceivers]uint32
	stop    chan struct{}
	wg      sync.WaitGroup
	sawGen  bool
}

func newFakeP2(t *testing.T, base int) *fakeP2 {
	f := &fakeP2{t: t, base: base, stop: make(chan struct{})}
	listen := func(port int) *net.UDPConn {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			t.Skipf("UDP port %d on loopback is not free: %v", port, err)
		}
		return c
	}
	f.gen, f.rx, f.hi = listen(base), listen(base+p2OffRx), listen(base+p2OffHigh)
	for d := 0; d < 3; d++ {
		f.data[d] = listen(base + p2OffData + d)
	}
	f.wg.Add(4)
	go f.loopGen()
	go f.loopRx()
	go f.loopHi()
	go f.stream()
	return f
}

func (f *fakeP2) close() {
	close(f.stop)
	f.gen.Close()
	f.rx.Close()
	f.hi.Close()
	for _, c := range f.data {
		if c != nil {
			c.Close()
		}
	}
	f.wg.Wait()
}

func (f *fakeP2) loopGen() {
	defer f.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, from, err := f.gen.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n == 60 && buf[4] == 2 && buf[0]|buf[1]|buf[2]|buf[3] == 0 { // discovery
			r := make([]byte, 60)
			r[4] = 2
			copy(r[5:], []byte{0xaa, 0xbb, 0xcc, 0, 0, 1})
			r[11], r[12], r[13], r[20] = 10, 39, 30, 10
			f.gen.WriteToUDP(r, from)
			continue
		}
		if n == 60 && buf[4] == 0 { // general: the host's address for every stream
			f.mu.Lock()
			f.host, f.sawGen = from, buf[37] == 0x08
			f.mu.Unlock()
		}
	}
}

func (f *fakeP2) loopRx() {
	defer f.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, _, err := f.rx.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n != 1444 {
			continue
		}
		f.mu.Lock()
		for d := 0; d < MaxReceivers; d++ {
			f.on[d] = buf[7]&(1<<uint(d)) != 0
		}
		f.rateKHz = int(binary.BigEndian.Uint16(buf[18:]))
		f.mu.Unlock()
	}
}

func (f *fakeP2) loopHi() {
	defer f.wg.Done()
	buf := make([]byte, 2048)
	for {
		n, _, err := f.hi.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n != 1444 {
			continue
		}
		f.mu.Lock()
		f.run = buf[4]&1 != 0
		for d := 0; d < MaxReceivers; d++ {
			f.freq[d] = binary.BigEndian.Uint32(buf[9+4*d:])
		}
		f.mu.Unlock()
	}
}

func (f *fakeP2) stream() {
	defer f.wg.Done()
	var seq [MaxReceivers]uint32
	tk := time.NewTicker(5 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-tk.C:
		}
		f.mu.Lock()
		host, run, on := f.host, f.run, f.on
		f.mu.Unlock()
		if host == nil || !run {
			continue
		}
		for d := 0; d < 3; d++ {
			if !on[d] {
				continue
			}
			p := make([]byte, 16+6*238)
			binary.BigEndian.PutUint32(p, seq[d])
			binary.BigEndian.PutUint16(p[12:], 24)
			binary.BigEndian.PutUint16(p[14:], 238)
			for i := 0; i < 238; i++ { // I = receiver number (1, 2, 3) in 1/64, Q = 0, so each stream is told apart
				v := int32(d+1) << 17
				p[16+6*i], p[17+6*i], p[18+6*i] = byte(v>>16), byte(v>>8), byte(v)
			}
			seq[d]++
			f.data[d].WriteToUDP(p, host)
		}
	}
}

func TestP2TwoReceiversShareOneRadio(t *testing.T) {
	const base = 41024
	f := newFakeP2(t, base)
	defer f.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := func(freq float64) Options {
		return Options{Addr: "127.0.0.1", Port: base, Protocol: 2, RateHz: 48000, FreqHz: freq, MirrorIQ: true, Timeout: 800 * time.Millisecond}
	}
	a, err := Open(ctx, o(521000))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, o(14100000))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Receiver() != 1 || b.Receiver() != 2 {
		t.Fatalf("receivers %d and %d", a.Receiver(), b.Receiver())
	}
	if !a.Radio().Busy && a.Radio().Protocol != 2 {
		t.Errorf("radio: %+v", a.Radio())
	}
	read := func(s *Source) float32 {
		buf := make([]complex64, 200)
		done := make(chan struct{})
		var n int
		var rerr error
		go func() { n, rerr = s.Read(buf); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("receiver %d: no samples", s.Receiver())
		}
		if rerr != nil || n == 0 {
			t.Fatalf("read: %d %v", n, rerr)
		}
		return real(buf[0])
	}
	// the first receiver's stream is the one the fake labelled 1 (value 1<<17 / 2^23 = 1/64), the second's labelled 2
	if got := read(a); got != 1.0/64 {
		t.Errorf("receiver 1 got %v, want %v", got, 1.0/64)
	}
	if got := read(b); got != 2.0/64 {
		t.Errorf("receiver 2 got %v, want %v", got, 2.0/64)
	}
	time.Sleep(300 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sawGen || !f.run || !f.on[0] || !f.on[1] || f.on[2] || f.rateKHz != 48 {
		t.Errorf("radio state: general %v run %v on %v rate %d", f.sawGen, f.run, f.on, f.rateKHz)
	}
	want := func(hz float64) uint32 { return p2Phase(hz) }
	if f.freq[0] != want(521000) || f.freq[1] != want(14100000) {
		t.Errorf("frequencies %d %d, want %d %d", f.freq[0], f.freq[1], want(521000), want(14100000))
	}
}

func TestP2RefusesOtherProtocolAndRate(t *testing.T) {
	const base = 41124
	f := newFakeP2(t, base)
	defer f.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := Open(ctx, Options{Addr: "127.0.0.1", Port: base, Protocol: 2, RateHz: 48000, FreqHz: 521000, Timeout: 800 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := Open(ctx, Options{Addr: "127.0.0.1", Port: base, Protocol: 1, RateHz: 48000, FreqHz: 490000}); err == nil {
		t.Errorf("a protocol 1 channel joined a protocol 2 radio")
	}
	if _, err := Open(ctx, Options{Addr: "127.0.0.1", Port: base, Protocol: 2, RateHz: 96000, FreqHz: 490000}); err == nil {
		t.Errorf("a channel with another sample rate joined")
	}
}

// A stateful firewall lets the I/Q streams in only from ports the host has sent to: the session sends to each receiver's stream port.
func TestP2OpensTheStreamPorts(t *testing.T) {
	const base = 41224
	f := newFakeP2(t, base)
	defer f.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := Open(ctx, Options{Addr: "127.0.0.1", Port: base, Protocol: 2, RateHz: 48000, FreqHz: 521000, Timeout: 800 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	f.data[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := f.data[0].ReadFromUDP(make([]byte, 64))
	if err != nil || n == 0 {
		t.Fatalf("nothing was sent to the I/Q port of receiver 1: %v", err)
	}
	f.mu.Lock()
	host := f.host
	f.mu.Unlock()
	if host == nil || from.Port != host.Port {
		t.Errorf("the datagram came from port %v, the other packets from %v", from.Port, host)
	}
}
