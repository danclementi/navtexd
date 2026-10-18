package hpsdr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"navtex/internal/iq"
)

// Stats counts what arrived.
type Stats struct {
	Packets    uint64
	Lost       uint64 // packets missing from the sequence
	Reordered  uint64
	Overruns   uint64 // blocks dropped because the decoder fell behind
	BadPackets uint64
}

// A radio streams to one host only, but one stream carries up to 8 receivers (down-converters) that share the sample
// rate and the antenna. The channels of this program that use the same radio therefore share one session: it holds the
// socket, asks the radio for as many receivers as are in use, sets each receiver's frequency and splits the interleaved
// samples into one stream per channel. A channel's Source is its receiver.
type session struct {
	key   string
	conn  *net.UDPConn
	raddr *net.UDPAddr
	radio Radio
	rate  int
	gap   time.Duration
	quiet time.Duration

	mu           sync.Mutex
	slots        [MaxReceivers]*Source // receiver k is slots[k-1]; nil = free
	nrx          int                   // receivers the radio is asked for
	discardUntil time.Time             // after a change of the receiver count the packets in flight have the old layout
	reset        bool
	seq          uint32
	rr           int // receiver whose frequency goes in the next command packet

	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
	err  atomic.Value // error
	st   struct{ packets, lost, reordered, overruns, bad atomic.Uint64 }
}

var (
	openMu sync.Mutex // one radio is opened at a time, so that channels starting together share a session
	regMu  sync.Mutex
	reg    = map[string]*session{}
)

func sessionKey(ip net.IP, port int) string { return net.JoinHostPort(ip.String(), fmt.Sprint(port)) }

// Source is one receiver of an open radio (one channel). It implements iq.Source.
type Source struct {
	opt  Options
	s    *session
	slot int
	ch   chan []complex64
	done chan struct{}
	once sync.Once
	cur  []complex64
	got  bool // data has arrived (OnData called)
}

// Open finds the radio (unless Addr is given), checks that it is free (or already used by another channel of this
// program, which then shares it), sends the settings and starts the stream.
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
	openMu.Lock()
	defer openMu.Unlock()
	// a radio that another channel of this program already streams from needs no search
	if o.Addr != "" {
		if ra, err := resolve(o.Addr, o.Port); err == nil {
			if s := lookup(ra); s != nil {
				return s.join(ctx, o)
			}
		}
	}
	radios, note, err := discover(ctx, o.Addr, o.Port, o.Timeout)
	if err != nil {
		return nil, err
	}
	if len(radios) == 0 {
		if o.Addr != "" {
			return nil, fmt.Errorf("no HPSDR radio answered at %s: %s (check the address, that the radio is on, and that nothing blocks UDP port %d)", o.Addr, note, o.Port)
		}
		return nil, errors.New("no HPSDR radio found on the local network; set input.radio to its address")
	}
	for i := range radios { // a radio busy with a channel of ours is not busy
		if lookup(radios[i].Addr) != nil {
			radios[i].Busy = false
		}
	}
	r, why, err := Choose(radios, o.Known)
	if err != nil {
		return nil, err
	}
	if why != "" && o.Chosen != nil {
		o.Chosen(fmt.Sprintf("radio %s chosen: %s", r, why))
	}
	if s := lookup(r.Addr); s != nil {
		return s.join(ctx, o)
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
	s := &session{key: sessionKey(r.Addr.IP, o.Port), conn: conn, raddr: r.Addr, radio: r, rate: o.RateHz, gap: o.SendGap, quiet: o.Quiet,
		nrx: 1, seq: o.StartSeq, done: make(chan struct{})}
	m := s.newMember(o, 0)
	s.slots[0] = m
	regMu.Lock()
	reg[s.key] = s
	regMu.Unlock()
	s.sendSettings(3)
	if _, err := conn.WriteToUDP(startPacket(true), s.raddr); err != nil {
		s.remove(m)
		return nil, err
	}
	s.wg.Add(2)
	go s.receive()
	go s.keepAlive()
	m.watch(ctx)
	return m, nil
}

// lookup returns the session streaming from the radio at addr, if any.
func lookup(a *net.UDPAddr) *session {
	regMu.Lock()
	defer regMu.Unlock()
	for _, s := range reg {
		if s.raddr.IP.Equal(a.IP) {
			return s
		}
	}
	return nil
}

func (s *session) newMember(o Options, slot int) *Source {
	return &Source{opt: o, s: s, slot: slot, ch: make(chan []complex64, 512), done: make(chan struct{})}
}

// join adds a receiver for another channel.
func (s *session) join(ctx context.Context, o Options) (*Source, error) {
	if o.RateHz != s.rate {
		return nil, fmt.Errorf("the radio %s already streams at %d Hz for another channel; all channels that share a radio need the same sample rate (not %d Hz)", s.radio.Addr.IP, s.rate, o.RateHz)
	}
	regMu.Lock()
	s.mu.Lock()
	if s.isDone() {
		s.mu.Unlock()
		regMu.Unlock()
		return nil, errors.New("the radio stream has just ended; try again")
	}
	slot := -1
	for i, m := range s.slots {
		if m == nil {
			slot = i
			break
		}
	}
	if slot < 0 {
		s.mu.Unlock()
		regMu.Unlock()
		return nil, fmt.Errorf("all %d receivers of the radio %s are in use by other channels", MaxReceivers, s.radio.Addr.IP)
	}
	m := s.newMember(o, slot)
	s.slots[slot] = m
	grow := slot+1 > s.nrx
	if grow {
		s.nrx = slot + 1
		s.discardUntil = time.Now().Add(500 * time.Millisecond)
		s.reset = true
	}
	n := s.nrx
	s.mu.Unlock()
	regMu.Unlock()
	if grow {
		// the layout of the packets changes: restart the stream cleanly
		for i := 0; i < 2; i++ {
			s.conn.WriteToUDP(startPacket(false), s.raddr)
		}
		time.Sleep(150 * time.Millisecond)
		s.sendSettings(3)
		s.conn.WriteToUDP(startPacket(true), s.raddr)
	} else {
		s.sendSettings(2)
	}
	_ = n
	m.watch(ctx)
	return m, nil
}

// sendSettings sends the general settings and the frequency of every receiver in use, rounds times.
func (s *session) sendSettings(rounds int) {
	for r := 0; r < rounds; r++ {
		s.mu.Lock()
		n := s.nrx
		var freqs [MaxReceivers]float64
		var used [MaxReceivers]bool
		for i, m := range s.slots {
			if m != nil {
				freqs[i], used[i] = m.opt.FreqHz, true
			}
		}
		s.mu.Unlock()
		for k := 0; k < n; k++ {
			if !used[k] {
				continue
			}
			s.sendOne(n, k, freqs[k])
		}
	}
}

func (s *session) sendOne(n, k int, f float64) {
	s.mu.Lock()
	seq := s.seq
	s.seq++
	s.mu.Unlock()
	if p, err := commandPacketN(seq, s.rate, n, k, f); err == nil {
		s.conn.WriteToUDP(p, s.raddr)
	}
}

// keepAlive repeats the settings, one receiver's frequency at a time, so that a radio that was restarted or lost them is
// corrected within a moment.
func (s *session) keepAlive() {
	defer s.wg.Done()
	t := time.NewTicker(s.gap)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.mu.Lock()
			n := s.nrx
			var f float64
			k := -1
			for i := 0; i < n; i++ {
				c := (s.rr + i) % n
				if m := s.slots[c]; m != nil {
					k, f = c, m.opt.FreqHz
					s.rr = c + 1
					break
				}
			}
			s.mu.Unlock()
			if k >= 0 {
				s.sendOne(n, k, f)
			}
		case <-s.done:
			return
		}
	}
}

func (s *session) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// notice sends a message to the first channel that uses the radio (the loss is the radio's, not a channel's).
func (s *session) notice(format string, a ...any) {
	s.mu.Lock()
	var to chan<- string
	for _, m := range s.slots {
		if m != nil && m.opt.Notices != nil {
			to = m.opt.Notices
			break
		}
	}
	s.mu.Unlock()
	if to == nil {
		return
	}
	select {
	case to <- fmt.Sprintf(format, a...):
	default:
	}
}

func perPacket(nrx int) int { return 2 * (frameData / (6*nrx + 2)) }

func (s *session) receive() {
	defer s.wg.Done()
	defer func() { // the channels of a session that ends read the error or EOF
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
				if gapsSince > 0 && time.Since(burstTo) > s.quiet {
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
		s.mu.Lock()
		nrx := s.nrx
		if time.Now().Before(s.discardUntil) {
			s.mu.Unlock()
			lastData = time.Now()
			continue
		}
		if s.reset {
			s.reset, started = false, false
		}
		s.mu.Unlock()
		pp := perPacket(nrx)
		dst := make([][]complex64, nrx)
		for i := range dst {
			dst[i] = make([]complex64, 0, pp)
		}
		p, err := ParseDataN(buf[:n], nrx, dst)
		if err != nil {
			s.st.bad.Add(1)
			continue
		}
		lastData = time.Now()
		s.st.packets.Add(1)
		var fill int
		if started && p.Seq != next {
			if int32(p.Seq-next) < 0 { // late or duplicate
				s.st.reordered.Add(1)
				continue
			}
			gap := p.Seq - next
			s.st.lost.Add(uint64(gap))
			lostSince += uint64(gap)
			gapsSince++
			if float64(gap)*float64(pp) <= maxFillS*float64(s.rate) {
				fill = int(gap) * pp // zeros keep the time axis continuous
			}
			if gapsSince == 1 {
				burstFrom = time.Now()
			}
			burstTo = time.Now()
		}
		if gapsSince > 0 && time.Since(burstTo) > s.quiet {
			flush()
		} else if gapsSince > 0 && time.Since(burstFrom) > 10*s.quiet {
			flush() // a loss that never stops is reported anyway, every so often
		}
		started = true
		next = p.Seq + 1
		var first []*Source
		s.mu.Lock()
		for k := 0; k < nrx && k < MaxReceivers; k++ {
			m := s.slots[k]
			if m == nil {
				continue
			}
			if fill > 0 {
				m.push(make([]complex64, fill), s)
			}
			blk := dst[k]
			if m.opt.SwapIQ {
				for i, v := range blk {
					blk[i] = complex(imag(v), real(v))
				}
			}
			m.push(blk, s)
			if !m.got {
				m.got = true
				first = append(first, m)
			}
		}
		s.mu.Unlock()
		for _, m := range first {
			if m.opt.OnData != nil {
				m.opt.OnData(s.radio)
			}
		}
	}
}

func (m *Source) push(b []complex64, s *session) {
	select {
	case m.ch <- b:
	default:
		s.st.overruns.Add(1)
	}
}

// watch releases the receiver when ctx ends (never waits: Close does that).
func (m *Source) watch(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			m.leave()
		case <-m.done:
		}
	}()
}

// leave gives the receiver back; the last one to leave stops the radio.
func (m *Source) leave() {
	m.once.Do(func() {
		close(m.done)
		m.s.remove(m)
	})
}

// remove takes a receiver out; when none is left the session ends.
func (s *session) remove(m *Source) {
	regMu.Lock()
	s.mu.Lock()
	if s.slots[m.slot] == m {
		s.slots[m.slot] = nil
	}
	empty := true
	for _, x := range s.slots {
		if x != nil {
			empty = false
		}
	}
	if empty && reg[s.key] == s {
		delete(reg, s.key)
	}
	s.mu.Unlock()
	regMu.Unlock()
	if empty {
		s.shutdown()
	}
}

// shutdown tells the radio to stop and closes the socket; it is safe to call more than once and from the session's own
// goroutines (it does not wait for them).
func (s *session) shutdown() {
	s.once.Do(func() {
		close(s.done)
		for i := 0; i < 3; i++ {
			s.conn.WriteToUDP(startPacket(false), s.raddr)
		}
		s.conn.Close()
	})
}

// Radio returns what the radio reported about itself.
func (m *Source) Radio() Radio { return m.s.radio }

// Stats returns the packet counters (of the whole radio stream, shared by its channels).
func (m *Source) Stats() Stats {
	s := m.s
	return Stats{s.st.packets.Load(), s.st.lost.Load(), s.st.reordered.Load(), s.st.overruns.Load(), s.st.bad.Load()}
}

// Receiver is the number of this channel's receiver in the radio (1 = the first).
func (m *Source) Receiver() int { return m.slot + 1 }

// iq.Source methods.

func (m *Source) SampleRate() float64 { return float64(m.s.rate) }
func (m *Source) CentreHz() float64   { return m.opt.FreqHz }
func (m *Source) Kind() iq.Kind       { return iq.IQ }

// Read returns the next samples, blocking until some arrive. It returns io.EOF after Close and an error if the radio
// goes silent.
func (m *Source) Read(dst []complex64) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(m.cur) == 0 {
		select {
		case b, ok := <-m.ch:
			if !ok {
				if e, _ := m.s.err.Load().(error); e != nil {
					return 0, e
				}
				return 0, io.EOF
			}
			m.cur = b
		case <-m.done:
			return 0, io.EOF
		}
	}
	n := copy(dst, m.cur)
	m.cur = m.cur[n:]
	return n, nil
}

// Close gives the receiver back (the last one stops the radio's stream and releases the socket, and waits for the
// session's goroutines to finish).
func (m *Source) Close() error {
	m.leave()
	if m.s.isDone() {
		m.s.wg.Wait()
	}
	return nil
}
