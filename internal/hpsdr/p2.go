package hpsdr

// Protocol 2 (the "new protocol"): several UDP ports instead of one stream, a fixed-layout register packet per port, and
// one I/Q stream per receiver (DDC) from its own source port. Receive only: nothing here sets the transmit bit (the
// run/MOX byte carries only the run bit), a drive level, the power-amplifier enable or an antenna-tuner setting.
//
// All packets go out from the one socket of the session and the radio answers every stream to that source port, telling
// the streams apart by its own source port (1035 + DDC for I/Q).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"time"
)

const (
	// The ports are the general port (1024, where discovery goes too) plus an offset, so that a test can use others.
	p2OffRx   = 1    // 1025: receiver-specific (DDC) registers
	p2OffHigh = 3    // 1027: high priority: run bit, frequencies
	p2OffData = 11   // 1035: I/Q of DDC n comes from this port + n
	p2Big     = 1444 // the receiver-specific and the high-priority packets
	p2Small   = 60   // discovery and the general packet
	p2Clock   = 122880000.0
)

// discoveryPacket2 is the Protocol 2 discovery request: 60 bytes, zeros but byte 4 = 2.
func discoveryPacket2() []byte {
	p := make([]byte, p2Small)
	p[4] = 2
	return p
}

// ParseDiscoveryReply2 decodes a Protocol 2 discovery reply; ok is false if b is not one (a running data stream has 1444 bytes).
func ParseDiscoveryReply2(b []byte, from *net.UDPAddr) (Radio, bool) {
	if len(b) < 14 || len(b) > 200 || b[0] != 0 || b[1] != 0 || b[2] != 0 || b[3] != 0 || (b[4] != 2 && b[4] != 3) {
		return Radio{}, false
	}
	r := Radio{Addr: from, MAC: net.HardwareAddr(append([]byte(nil), b[5:11]...)), Board: int(b[11]), Firmware: int(b[13]), Busy: b[4] == 3, Protocol: 2}
	r.Name = boardName2(r.Board)
	return r, true
}

func boardName2(id int) string {
	switch id {
	case 0:
		return "Atlas"
	case 1:
		return "Hermes"
	case 2:
		return "Hermes 2"
	case 3:
		return "Angelia"
	case 4:
		return "Orion"
	case 5:
		return "Orion Mk II"
	case 6:
		return "Hermes Lite"
	case 10:
		return "Saturn"
	}
	return fmt.Sprintf("HPSDR board %d", id)
}

// p2General is the general-registers packet: every port left at its default, the frequencies as phase words, the hardware timer
// on (as other programs send it). The power amplifier and the filter boards are left off.
func p2General(seq uint32) []byte {
	p := make([]byte, p2Small)
	binary.BigEndian.PutUint32(p, seq)
	p[37] = 0x08 // frequency words are phase increments
	p[38] = 0x01 // hardware timer
	return p
}

// p2RxSpecific is the receiver-specific packet: the receivers (DDCs) that are on, each from ADC 0 at the given rate (kHz), 24 bits.
func p2RxSpecific(seq uint32, rateKHz int, on [MaxReceivers]bool) []byte {
	p := make([]byte, p2Big)
	binary.BigEndian.PutUint32(p, seq)
	p[4] = 1 // one ADC
	for d, e := range on {
		if !e {
			continue
		}
		p[7] |= 1 << uint(d)
		p[17+6*d] = 0 // ADC 0
		binary.BigEndian.PutUint16(p[18+6*d:], uint16(rateKHz))
		p[22+6*d] = 24
	}
	return p
}

// p2Phase is a frequency as the phase word of the 122.88 MHz clock.
func p2Phase(hz float64) uint32 { return uint32(math.Round(hz * 4294967296.0 / p2Clock)) }

// p2High is the high-priority packet: the run bit (never MOX) and each receiver's frequency. The transmit frequency, the drive
// level, the Alex words and the attenuators stay zero.
func p2High(seq uint32, run bool, hz [MaxReceivers]float64, on [MaxReceivers]bool) []byte {
	p := make([]byte, p2Big)
	binary.BigEndian.PutUint32(p, seq)
	if run {
		p[4] = 1
	}
	for d := range on {
		if on[d] {
			binary.BigEndian.PutUint32(p[9+4*d:], p2Phase(hz[d]))
		}
	}
	return p
}

// ParseData2 decodes the packet of one receiver's I/Q stream, appending its samples to dst.
func ParseData2(b []byte, dst []complex64) (seq uint32, out []complex64, err error) {
	if len(b) < 16 {
		return 0, dst, errors.New("short I/Q packet")
	}
	if bits := binary.BigEndian.Uint16(b[12:]); bits != 24 {
		return 0, dst, fmt.Errorf("%d bits per sample, not 24", bits)
	}
	n := int(binary.BigEndian.Uint16(b[14:]))
	if n < 0 || 16+6*n > len(b) {
		return 0, dst, fmt.Errorf("packet of %d bytes cannot hold %d samples", len(b), n)
	}
	seq = binary.BigEndian.Uint32(b)
	for i := 0; i < n; i++ {
		o := b[16+6*i:]
		dst = append(dst, complex(s24(o[0:3]), s24(o[3:6])))
	}
	return seq, dst, nil
}

// ---- the session side of protocol 2 ----

func (s *session) p2Addr(off int) *net.UDPAddr {
	return &net.UDPAddr{IP: s.raddr.IP, Port: s.raddr.Port + off}
}

func (s *session) nextHi() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.seqHi
	s.seqHi++
	return n
}

// snapshot returns the receivers in use and their frequencies.
func (s *session) snapshot() (on [MaxReceivers]bool, hz [MaxReceivers]float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.slots {
		if m != nil {
			hz[i] = m.opt.FreqHz
			on[i] = true
		}
	}
	return
}

// sendP2 sends the receiver-specific packet (rx) and the high-priority packet (hi, with the run bit).
func (s *session) sendP2(rx, hi bool) {
	on, hz := s.snapshot()
	if rx {
		s.mu.Lock()
		n := s.seqRx
		s.seqRx++
		s.mu.Unlock()
		s.conn.WriteToUDP(p2RxSpecific(n, s.rate/1000, on), s.p2Addr(p2OffRx))
	}
	if hi {
		s.conn.WriteToUDP(p2High(s.nextHi(), true, hz, on), s.p2Addr(p2OffHigh))
	}
}

func (s *session) sendGeneral() {
	s.mu.Lock()
	n := s.seq
	s.seq++
	s.mu.Unlock()
	s.conn.WriteToUDP(p2General(n), s.p2Addr(0))
}

// punch sends a tiny datagram from the session's socket to the port each receiver's I/Q will come from. A stateful firewall or
// router (a network with a security policy between this computer and the radio) lets a reply in only from an address and port
// this computer has already sent to; the I/Q streams come from ports (1035 and up) that nothing was sent to, so they are
// dropped as unsolicited and a radio that works on the same network sends nothing across the router. After this datagram the
// stream is a reply. The radio has nothing listening there and ignores it.
func (s *session) punch() {
	on, _ := s.snapshot()
	for d, e := range on {
		if e {
			s.conn.WriteToUDP([]byte{0, 0, 0, 0}, s.p2Addr(p2OffData+d))
		}
	}
}

// startP2 sends the three set-up packets in the order the radio expects, then the run bit.
func (s *session) startP2() error {
	if _, err := s.conn.WriteToUDP(p2General(s.nextGen()), s.p2Addr(0)); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	s.sendP2(true, false)
	s.punch()
	time.Sleep(50 * time.Millisecond)
	s.sendP2(false, true)
	return nil
}

func (s *session) nextGen() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.seq
	s.seq++
	return n
}

// keepAlive2 repeats the packets as other programs do: high priority every 100 ms, receiver-specific every 200 ms, general
// every 800 ms, so that a radio that was restarted or lost them is corrected within a moment.
func (s *session) keepAlive2() {
	defer s.wg.Done()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-t.C:
			if tick%8 == 0 {
				s.sendGeneral()
			}
			s.sendP2(tick%2 == 0, true)
			if tick%5 == 0 { // every half second, which also keeps a router's entry for the stream alive
				s.punch()
			}
		case <-s.done:
			return
		}
	}
}

// receive2 reads the I/Q streams of the receivers (each from its own source port) and hands them to the channels.
func (s *session) receive2() {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		for _, m := range s.slots {
			if m != nil {
				close(m.ch)
			}
		}
		s.slots = [MaxReceivers]*Source{}
		s.mu.Unlock()
	}()
	buf := make([]byte, 2048)
	var next [MaxReceivers]uint32
	var started [MaxReceivers]bool
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
	lastData, began := time.Now(), time.Now()
	var gotData, heard, hinted bool
	errs := 0
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
				if !gotData && !hinted && time.Since(began) > 5*time.Second {
					hinted = true
					if heard {
						s.notice("radio: the radio answers but sends no I/Q yet; check that its receivers, the sample rate and the frequencies are accepted")
					} else {
						s.notice("radio: nothing at all has come back from the radio (%s) since the start; a firewall or router between this computer and the radio may be dropping its UDP replies (it sends from ports 1024 to 1042)", s.radio.Addr.IP)
					}
				}
				if gapsSince > 0 && time.Since(burstTo) > s.quiet {
					flush()
				}
				if time.Since(lastData) > silenceFail {
					s.err.Store(fmt.Errorf("the radio (%s) has stopped sending data", s.radio.Addr.IP))
					return
				}
				continue
			}
			// a datagram sent to a port nobody listens on can come back as an error on this socket (Windows reports it on
			// the next read): not a reason to stop
			if errors.Is(err, net.ErrClosed) || errs > 1000 {
				s.err.Store(err)
				return
			}
			errs++
			continue
		}
		errs = 0
		if from.IP.Equal(s.raddr.IP) {
			heard = true
		}
		d := from.Port - s.raddr.Port - p2OffData
		if !from.IP.Equal(s.raddr.IP) || d < 0 || d >= MaxReceivers {
			continue // the status, microphone and wideband streams are not used
		}
		var pk []complex64
		seq, pk, err := ParseData2(buf[:n], nil)
		if err != nil {
			s.st.bad.Add(1)
			continue
		}
		s.mu.Lock()
		m := s.slots[d]
		s.mu.Unlock()
		if m == nil {
			continue
		}
		lastData, gotData = time.Now(), true
		s.st.packets.Add(1)
		fill := 0
		restart := false
		if started[d] && seq != next[d] {
			if dd := int32(seq - next[d]); dd < 0 {
				if dd >= -maxLate {
					s.st.reordered.Add(1)
					continue
				}
				restart = true
				s.notice("radio: the packet counter of receiver %d started again (expected %d, got %d); continuing from here", d+1, next[d], seq)
			}
		}
		if started[d] && seq != next[d] && !restart {
			gap := seq - next[d]
			s.st.lost.Add(uint64(gap))
			lostSince += uint64(gap)
			gapsSince++
			if float64(gap)*float64(len(pk)) <= maxFillS*float64(s.rate) {
				fill = int(gap) * len(pk)
			}
			if gapsSince == 1 {
				burstFrom = time.Now()
			}
			burstTo = time.Now()
		}
		if gapsSince > 0 && time.Since(burstTo) > s.quiet {
			flush()
		} else if gapsSince > 0 && time.Since(burstFrom) > 10*s.quiet {
			flush()
		}
		started[d] = true
		next[d] = seq + 1
		if fill > 0 {
			m.push(make([]complex64, fill), s)
		}
		if !m.opt.MirrorIQ { // the same exchange as with protocol 1 (see Options.MirrorIQ)
			for i, v := range pk {
				pk[i] = complex(imag(v), real(v))
			}
		}
		m.push(pk, s)
		s.mu.Lock()
		first := !m.got
		m.got = true
		s.mu.Unlock()
		if first && m.opt.OnData != nil {
			m.opt.OnData(s.radio)
		}
	}
}
