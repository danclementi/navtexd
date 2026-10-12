// Package hpsdr receives I/Q samples from an OpenHPSDR "Protocol 1" (Metis / Hermes) radio over UDP: Hermes, Hermes
// Lite, ANAN, and Pavel Demin's Red Pitaya and QMtech images. It is receive-only: nothing here ever sets the transmit
// (MOX) bit or writes a transmit-related setting. See docs/HPSDR_DESIGN.md for the packet layouts.
package hpsdr

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"navtex/internal/iq"
)

// DefaultPort is the UDP port every Protocol 1 radio listens on.
const DefaultPort = 1024

const (
	packetLen   = 1032 // every command and data packet
	frameLen    = 512  // two frames per packet
	frameData   = 504  // payload bytes in a frame, after the 8-byte sync + C&C header
	sendEvery   = 100 * time.Millisecond
	silenceFail = 10 * time.Second
	busyWait    = 3 * time.Second
	busyStep    = 300 * time.Millisecond
	maxFillS    = 1.0 // a gap longer than this is not filled with zeros
)

// Radio is what a discovery reply tells about a radio.
type Radio struct {
	Addr     *net.UDPAddr
	MAC      net.HardwareAddr
	Firmware int
	Board    int // 1 = Hermes (also reported by Pavel Demin's images), 6 = Hermes Lite, ...
	Name     string
	Busy     bool // already streaming to some host
}

func (r Radio) String() string {
	name := r.Name
	if name == "" {
		name = boardName(r.Board)
	}
	s := fmt.Sprintf("%s at %s (MAC %s, firmware %d)", name, r.Addr.IP, r.MAC, r.Firmware)
	if r.Busy {
		s += ", busy"
	}
	return s
}

func boardName(id int) string {
	switch id {
	case 0:
		return "Metis"
	case 1:
		return "Hermes"
	case 2:
		return "Griffin"
	case 4:
		return "Angelia"
	case 5:
		return "Orion"
	case 6:
		return "Hermes Lite"
	case 10:
		return "Orion Mk II"
	}
	return fmt.Sprintf("HPSDR board %d", id)
}

// ParseDiscoveryReply decodes a discovery reply; ok is false if b is not one.
func ParseDiscoveryReply(b []byte, from *net.UDPAddr) (Radio, bool) {
	if len(b) < 11 || b[0] != 0xef || b[1] != 0xfe || (b[2] != 2 && b[2] != 3) {
		return Radio{}, false
	}
	r := Radio{Addr: from, MAC: net.HardwareAddr(append([]byte(nil), b[3:9]...)), Firmware: int(b[9]), Board: int(b[10]), Busy: b[2] == 3}
	// Pavel Demin's images append a name; it is only used when printable.
	if len(b) >= 19 {
		n := b[11:19]
		end := 0
		for end < len(n) && n[end] >= 32 && n[end] < 127 {
			end++
		}
		r.Name = string(n[:end])
	}
	return r, true
}

func discoveryPacket() []byte {
	p := make([]byte, 60)
	p[0], p[1], p[2] = 0xef, 0xfe, 0x02
	return p
}

// Discover looks for radios. If addr is not empty it asks only that host (a bare address or host:port); otherwise it
// broadcasts on every network interface. It collects replies until timeout.
func Discover(ctx context.Context, addr string, port int, timeout time.Duration) ([]Radio, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var targets []*net.UDPAddr
	if addr != "" {
		t, err := resolve(addr, port)
		if err != nil {
			return nil, err
		}
		targets = []*net.UDPAddr{t}
	} else {
		targets = broadcastTargets(port)
	}
	pkt := discoveryPacket()
	for _, t := range targets {
		conn.WriteToUDP(pkt, t) // an interface that cannot send is simply skipped
	}
	var found []Radio
	seen := map[string]bool{}
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if r, ok := ParseDiscoveryReply(buf[:n], from); ok && !seen[r.MAC.String()] {
			seen[r.MAC.String()] = true
			found = append(found, r)
		}
	}
	return found, nil
}

func resolve(addr string, port int) (*net.UDPAddr, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, fmt.Sprint(port))
	}
	t, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, fmt.Errorf("radio address %q: %w", addr, err)
	}
	return t, nil
}

func broadcastTargets(port int) []*net.UDPAddr {
	targets := []*net.UDPAddr{{IP: net.IPv4bcast, Port: port}}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip, mask := ipn.IP.To4(), net.IP(ipn.Mask).To4()
			if mask == nil {
				continue
			}
			bc := make(net.IP, 4)
			for i := range bc {
				bc[i] = ip[i] | ^mask[i]
			}
			targets = append(targets, &net.UDPAddr{IP: bc, Port: port})
		}
	}
	return targets
}

// rateCode maps a sample rate to the 2-bit code in the general command.
func rateCode(rate int) (byte, error) {
	switch rate {
	case 48000:
		return 0, nil
	case 96000:
		return 1, nil
	case 192000:
		return 2, nil
	case 384000:
		return 3, nil
	}
	return 0, fmt.Errorf("sample rate %d Hz: the radio protocol offers 48000, 96000, 192000 or 384000", rate)
}

// CommandPacket builds one host-to-radio packet carrying the general settings (sample rate, one receiver) in its first
// frame and the receiver-1 frequency in its second. The transmit data bytes are zero and C0 bit 0 (MOX) is always 0.
func CommandPacket(seq uint32, rate int, freqHz float64) ([]byte, error) {
	rc, err := rateCode(rate)
	if err != nil {
		return nil, err
	}
	if freqHz < 0 || freqHz > 4e9 {
		return nil, fmt.Errorf("radio frequency %.0f Hz out of range", freqHz)
	}
	p := make([]byte, packetLen)
	p[0], p[1], p[2], p[3] = 0xef, 0xfe, 0x01, 0x02
	binary.BigEndian.PutUint32(p[4:], seq)
	for i := 0; i < 2; i++ {
		f := p[8+i*frameLen:]
		f[0], f[1], f[2] = 0x7f, 0x7f, 0x7f
	}
	g := p[8:] // frame 1: C0 = 0 general settings
	g[3] = 0x00
	g[4] = rc // C1: sample rate
	g[7] = 0  // C4: bits 5:3 = receivers - 1 = 0
	f := p[8+frameLen:]
	f[3] = 0x04 // C0: receiver-1 frequency
	binary.BigEndian.PutUint32(f[4:], uint32(freqHz+0.5))
	return p, nil
}

func startPacket(on bool) []byte {
	p := make([]byte, 64)
	p[0], p[1], p[2] = 0xef, 0xfe, 0x04
	if on {
		p[3] = 0x01 // I/Q stream only
	}
	return p
}

// Parsed is the content of one radio-to-host data packet.
type Parsed struct {
	Seq     uint32
	Samples []complex64 // receiver 1 only
	Status  [2][5]byte  // C0..C4 of each frame
}

// ParseData decodes a data packet carrying nrx receivers, appending receiver 1's samples to dst.
func ParseData(b []byte, nrx int, dst []complex64) (Parsed, []complex64, error) {
	var p Parsed
	if len(b) != packetLen || b[0] != 0xef || b[1] != 0xfe || b[2] != 0x01 || b[3] != 0x06 {
		return p, dst, errors.New("not an I/Q data packet")
	}
	if nrx < 1 || nrx > 8 {
		return p, dst, fmt.Errorf("receivers %d", nrx)
	}
	p.Seq = binary.BigEndian.Uint32(b[4:])
	group := 6*nrx + 2
	n := frameData / group
	for i := 0; i < 2; i++ {
		f := b[8+i*frameLen : 8+(i+1)*frameLen]
		if f[0] != 0x7f || f[1] != 0x7f || f[2] != 0x7f {
			return p, dst, errors.New("lost frame sync")
		}
		copy(p.Status[i][:], f[3:8])
		d := f[8:]
		for k := 0; k < n; k++ {
			g := d[k*group:]
			dst = append(dst, complex(s24(g[0:3]), s24(g[3:6])))
		}
	}
	p.Samples = dst
	return p, dst, nil
}

func s24(b []byte) float32 {
	v := int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
	if v&0x800000 != 0 {
		v -= 1 << 24
	}
	return float32(v) / 8388608
}

// Options describes the receiver to open.
type Options struct {
	Addr     string  // radio address; empty = discover
	Port     int     // UDP port; 0 = DefaultPort
	RateHz   int     // 48000, 96000, 192000 or 384000
	FreqHz   float64 // receiver frequency (the centre of the I/Q stream)
	SwapIQ   bool
	Timeout  time.Duration    // discovery wait; 0 = 1.5 s
	Notices  chan<- string    // optional: lost-packet and similar notices (never blocks)
	Known    []string         // addresses used successfully before, most recent first: preferred when a search finds several radios
	OnData   func(Radio)      // called once, when the first data arrives from the radio (it works)
	Chosen   func(why string) // called when a search picked one of several radios, with the reason
	Quiet    time.Duration    // lost packets are reported once this long has passed without another loss; 0 = 5 s
	SendGap  time.Duration    // interval between command packets; 0 = 100 ms
	StartSeq uint32
}

// Stats counts what arrived.
type Stats struct {
	Packets    uint64
	Lost       uint64 // packets missing from the sequence
	Reordered  uint64
	Overruns   uint64 // blocks dropped because the decoder fell behind
	BadPackets uint64
}

// Source is an open radio. It implements iq.Source.
type Source struct {
	opt   Options
	radio Radio
	conn  *net.UDPConn
	raddr *net.UDPAddr
	ch    chan []complex64
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup
	cur   []complex64
	err   atomic.Value // error
	st    struct{ packets, lost, reordered, overruns, bad atomic.Uint64 }
}

// Open finds the radio (unless Addr is given), checks that it is free, sends the settings and starts the stream.
func Open(ctx context.Context, o Options) (*Source, error) {
	if o.Port == 0 {
		o.Port = DefaultPort
	}
	if o.Timeout == 0 {
		o.Timeout = 1500 * time.Millisecond
	}
	if o.Quiet == 0 {
		o.Quiet = 5 * time.Second
	}
	if o.SendGap == 0 {
		o.SendGap = sendEvery
	}
	if _, err := CommandPacket(0, o.RateHz, o.FreqHz); err != nil {
		return nil, err
	}
	radios, err := Discover(ctx, o.Addr, o.Port, o.Timeout)
	if err != nil {
		return nil, err
	}
	if len(radios) == 0 {
		if o.Addr != "" {
			return nil, fmt.Errorf("no HPSDR radio answered at %s (check the address, that the radio is on, and that nothing blocks UDP port %d)", o.Addr, o.Port)
		}
		return nil, errors.New("no HPSDR radio found on the local network; set input.radio to its address")
	}
	r, why, err := Choose(radios, o.Known)
	if err != nil {
		return nil, err
	}
	if why != "" && o.Chosen != nil {
		o.Chosen(fmt.Sprintf("radio %s chosen: %s", r, why))
	}
	// A radio still winding down our own previous session (a restart after changing the sample rate, say) reports
	// itself busy for a moment; give it a few seconds before blaming another program.
	for wait := time.Duration(0); r.Busy && wait < busyWait && ctx.Err() == nil; wait += busyStep {
		time.Sleep(busyStep)
		if again, err := Discover(ctx, r.Addr.String(), o.Port, o.Timeout); err == nil && len(again) > 0 {
			r = again[0]
		}
	}
	if r.Busy {
		return nil, fmt.Errorf("%s is already streaming to another program (Thetis, SDRConnect, ...); close it first", r)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	// A large receive buffer lets a brief stall of this program (or of Windows) pass without dropping packets: the
	// default is small (tens of packets at the higher rates). The system may grant less; that is not an error.
	_ = conn.SetReadBuffer(4 << 20)
	s := &Source{opt: o, radio: r, conn: conn, raddr: r.Addr, ch: make(chan []complex64, 512), done: make(chan struct{}), cur: nil}
	seq := o.StartSeq
	send := func() error {
		p, err := CommandPacket(seq, o.RateHz, o.FreqHz)
		if err != nil {
			return err
		}
		seq++
		_, err = conn.WriteToUDP(p, s.raddr)
		return err
	}
	// settings first, then start
	for i := 0; i < 3; i++ {
		if err := send(); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if _, err := conn.WriteToUDP(startPacket(true), s.raddr); err != nil {
		conn.Close()
		return nil, err
	}
	s.wg.Add(2)
	go s.receive()
	go func() { // keep the settings fresh; a radio that was restarted or lost them is corrected within a moment
		defer s.wg.Done()
		t := time.NewTicker(o.SendGap)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				send()
			case <-s.done:
				return
			case <-ctx.Done():
				s.shutdown() // never Close here: Close waits for this goroutine
				return
			}
		}
	}()
	return s, nil
}

// Radio returns what the radio reported about itself.
func (s *Source) Radio() Radio { return s.radio }

// Stats returns the packet counters.
func (s *Source) Stats() Stats {
	return Stats{s.st.packets.Load(), s.st.lost.Load(), s.st.reordered.Load(), s.st.overruns.Load(), s.st.bad.Load()}
}

func (s *Source) notice(format string, a ...any) {
	if s.opt.Notices == nil {
		return
	}
	select {
	case s.opt.Notices <- fmt.Sprintf(format, a...):
	default:
	}
}

func (s *Source) receive() {
	defer s.wg.Done()
	defer close(s.ch)
	buf := make([]byte, 2048)
	perPacket := 2 * (frameData / 8)
	var next uint32
	started := false
	// Losses are reported as one line per burst, once the burst is over (Quiet without another loss), with its start and
	// end times (UTC, like the rest of the log), so that they can be compared with events on the PC or the network.
	var lostSince, gapsSince uint64
	var burstFrom, burstTo time.Time
	flush := func() {
		if gapsSince == 0 {
			return
		}
		s.notice("radio: %d packet(s) lost in %d gap(s) between %s and %s UTC; if this repeats, check the network and the PC's load",
			lostSince, gapsSince, burstFrom.UTC().Format("15:04:05"), burstTo.UTC().Format("15:04:05"))
		lostSince, gapsSince = 0, 0
	}
	defer flush()
	lastData := time.Now()
	for {
		s.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if gapsSince > 0 && time.Since(burstTo) > s.opt.Quiet {
					flush()
				}
				if time.Since(lastData) > silenceFail {
					s.err.Store(fmt.Errorf("the radio (%s) has stopped sending data", s.radio.Addr.IP))
					return
				}
				continue
			}
			s.err.Store(err)
			return
		}
		if !from.IP.Equal(s.raddr.IP) {
			continue
		}
		p, blk, err := ParseData(buf[:n], 1, make([]complex64, 0, perPacket))
		if err != nil {
			s.st.bad.Add(1)
			continue
		}
		lastData = time.Now()
		if s.st.packets.Add(1) == 1 && s.opt.OnData != nil {
			s.opt.OnData(s.radio)
		}
		if started && p.Seq != next {
			if int32(p.Seq-next) < 0 { // late or duplicate
				s.st.reordered.Add(1)
				continue
			}
			gap := p.Seq - next
			s.st.lost.Add(uint64(gap))
			lostSince += uint64(gap)
			gapsSince++
			if float64(gap)*float64(perPacket) <= maxFillS*float64(s.opt.RateHz) {
				fill := make([]complex64, int(gap)*perPacket) // zeros keep the time axis continuous
				s.push(fill)
			}
			if gapsSince == 1 {
				burstFrom = time.Now()
			}
			burstTo = time.Now()
		}
		if gapsSince > 0 && time.Since(burstTo) > s.opt.Quiet {
			flush()
		} else if gapsSince > 0 && time.Since(burstFrom) > 10*s.opt.Quiet {
			flush() // a loss that never stops is reported anyway, every so often
		}
		started = true
		next = p.Seq + 1
		if s.opt.SwapIQ {
			for i, v := range blk {
				blk[i] = complex(imag(v), real(v))
			}
		}
		s.push(blk)
	}
}

func (s *Source) push(b []complex64) {
	select {
	case s.ch <- b:
	default:
		s.st.overruns.Add(1)
	}
}

// iq.Source methods.

func (s *Source) SampleRate() float64 { return float64(s.opt.RateHz) }
func (s *Source) CentreHz() float64   { return s.opt.FreqHz }
func (s *Source) Kind() iq.Kind       { return iq.IQ }

// Read returns the next samples, blocking until some arrive. It returns io.EOF after Close and an error if the radio
// goes silent.
func (s *Source) Read(dst []complex64) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(s.cur) == 0 {
		select {
		case b, ok := <-s.ch:
			if !ok {
				if e, _ := s.err.Load().(error); e != nil {
					return 0, e
				}
				return 0, io.EOF
			}
			s.cur = b
		case <-s.done:
			return 0, io.EOF
		}
	}
	n := copy(dst, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// shutdown tells the radio to stop and closes the socket; it is safe to call more than once and from the source's own
// goroutines (it does not wait for them).
func (s *Source) shutdown() {
	s.once.Do(func() {
		close(s.done)
		for i := 0; i < 3; i++ {
			s.conn.WriteToUDP(startPacket(false), s.raddr)
		}
		s.conn.Close()
	})
}

// Close stops the radio's stream and releases the socket, and waits for the source's goroutines to finish.
func (s *Source) Close() error {
	s.shutdown()
	s.wg.Wait()
	return nil
}
