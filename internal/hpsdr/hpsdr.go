// Package hpsdr receives I/Q samples from an OpenHPSDR "Protocol 1" (Metis / Hermes) radio over UDP: Hermes, Hermes
// Lite, ANAN, and Pavel Demin's Red Pitaya and QMtech images. It is receive-only: nothing here ever sets the transmit
// (MOX) bit or writes a transmit-related setting. See docs/HPSDR_DESIGN.md for the packet layouts.
package hpsdr

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
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

// discoveryPacket is the 63-byte discovery request of the Metis / Hermes protocol (some radios and emulators insist on the
// full length).
func discoveryPacket() []byte {
	p := make([]byte, 63)
	p[0], p[1], p[2] = 0xef, 0xfe, 0x02
	return p
}

// Discover looks for radios. If addr is not empty it asks only that host (a bare address or host:port); otherwise it
// broadcasts on every network interface. It collects replies until timeout.
func Discover(ctx context.Context, addr string, port int, timeout time.Duration) ([]Radio, error) {
	found, _, err := discover(ctx, addr, port, timeout)
	return found, err
}

// discover is Discover plus a note about what else was seen when nothing answered properly (packets that are not
// discovery replies, or a refusal), to be shown with the error.
func discover(ctx context.Context, addr string, port int, timeout time.Duration) (found []Radio, note string, err error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	var targets []*net.UDPAddr
	if addr != "" {
		t, err := resolve(addr, port)
		if err != nil {
			return nil, "", err
		}
		targets = []*net.UDPAddr{t}
	} else {
		targets = broadcastTargets(port)
	}
	pkt := discoveryPacket()
	for _, t := range targets {
		conn.WriteToUDP(pkt, t) // an interface that cannot send is simply skipped
	}
	seen := map[string]bool{}
	var other, errs int
	var first string
	var lastErr error
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		conn.SetReadDeadline(deadline)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			// e.g. Windows reports "connection forcibly closed" when the host answers that nothing listens on the port
			lastErr = err
			if errs++; errs > 10 {
				break
			}
			continue
		}
		if r, ok := ParseDiscoveryReply(buf[:n], from); ok {
			if !seen[r.MAC.String()] {
				seen[r.MAC.String()] = true
				found = append(found, r)
			}
			if addr != "" {
				break // a radio asked by address answers once
			}
			continue
		}
		if other++; other == 1 {
			m := n
			if m > 12 {
				m = 12
			}
			first = fmt.Sprintf("%d bytes from %s, starting % x", n, from, buf[:m])
		}
	}
	switch {
	case len(found) > 0:
	case other > 0:
		note = fmt.Sprintf("something answered, but not with an HPSDR discovery reply (%d packet(s); first: %s)", other, first)
	case lastErr != nil:
		note = "the host answered that nothing listens on that UDP port (" + lastErr.Error() + ")"
	default:
		note = "no reply at all"
	}
	return found, note, nil
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

// MaxReceivers is the number of receivers (down-converters) one Protocol 1 stream can carry.
const MaxReceivers = 8

// CommandPacket builds one host-to-radio packet carrying the general settings (sample rate, one receiver) in its first
// frame and the receiver-1 frequency in its second. The transmit data bytes are zero and C0 bit 0 (MOX) is always 0.
func CommandPacket(seq uint32, rate int, freqHz float64) ([]byte, error) {
	return commandPacketN(seq, rate, 1, 0, freqHz)
}

// commandPacketN is CommandPacket for a stream of nrx receivers: the first frame sets the sample rate and the number of
// receivers, the second the frequency of receiver rx (0-based; C0 = 4 for receiver 1, 6 for receiver 2, ...).
func commandPacketN(seq uint32, rate, nrx, rx int, freqHz float64) ([]byte, error) {
	rc, err := rateCode(rate)
	if err != nil {
		return nil, err
	}
	if nrx < 1 || nrx > MaxReceivers || rx < 0 || rx >= nrx {
		return nil, fmt.Errorf("receiver %d of %d: a radio stream carries 1 to %d receivers", rx+1, nrx, MaxReceivers)
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
	g[4] = rc               // C1: sample rate
	g[7] = byte(nrx-1) << 3 // C4: bits 5:3 = receivers - 1
	f := p[8+frameLen:]
	f[3] = byte(4 + 2*rx) // C0: receiver frequency (never the transmit frequency, C0 = 2)
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

// ParseDataN decodes a data packet carrying nrx receivers and appends receiver k's samples to dst[k]. The samples of
// the receivers are interleaved: every group holds I and Q (3 bytes each) of receiver 1, 2, ... followed by 2 microphone bytes.
func ParseDataN(b []byte, nrx int, dst [][]complex64) (Parsed, error) {
	var p Parsed
	if len(b) != packetLen || b[0] != 0xef || b[1] != 0xfe || b[2] != 0x01 || b[3] != 0x06 {
		return p, errors.New("not an I/Q data packet")
	}
	if nrx < 1 || nrx > MaxReceivers || len(dst) < nrx {
		return p, fmt.Errorf("receivers %d", nrx)
	}
	p.Seq = binary.BigEndian.Uint32(b[4:])
	group := 6*nrx + 2
	n := frameData / group
	for i := 0; i < 2; i++ {
		f := b[8+i*frameLen : 8+(i+1)*frameLen]
		if f[0] != 0x7f || f[1] != 0x7f || f[2] != 0x7f {
			return p, errors.New("lost frame sync")
		}
		copy(p.Status[i][:], f[3:8])
		d := f[8:]
		for k := 0; k < n; k++ {
			g := d[k*group:]
			for r := 0; r < nrx; r++ {
				o := g[6*r:]
				dst[r] = append(dst[r], complex(s24(o[0:3]), s24(o[3:6])))
			}
		}
	}
	return p, nil
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
