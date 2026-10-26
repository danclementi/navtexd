// Package spyserver is a client for Airspy's SpyServer: it takes the I/Q stream of one channel from a SpyServer (Airspy HF+
// Discovery, Airspy One or RTL-SDR attached to it) over TCP. The wire format is the one of Airspy's spyserver_protocol.h
// (little endian, protocol 2.0.1700); see docs/SPYSERVER_DESIGN.md for what this program uses of it. Receive only.
package spyserver

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"navtex/internal/iq"
)

// DefaultAddr is where SpyServer listens by default.
const DefaultAddr = "127.0.0.1:5555"

const (
	protocolVersion = (2 << 24) | 1700
	maxBody         = 1 << 20

	cmdHello      = 0
	cmdSetSetting = 2

	setStreamingMode    = 0
	setStreamingEnabled = 1
	setGain             = 2
	setIQFormat         = 100
	setIQFrequency      = 101
	setIQDecimation     = 102

	msgDeviceInfo = 0
	msgClientSync = 1
	msgUint8IQ    = 100
	msgInt16IQ    = 101
	msgFloatIQ    = 103

	fmtUint8, fmtInt16, fmtFloat = 1, 2, 4

	streamIQOnly = 1
)

// TargetRateHz is the sample rate the stream is brought near: the decimation stage with the rate closest to it is used.
const TargetRateHz = 250000

var retryEvery = 5 * time.Second

// Options says what to ask the SpyServer for.
type Options struct {
	Addr    string
	LOHz    float64                  // centre frequency of the I/Q stream
	Gain    int                      // gain index; negative = leave as the server has it
	SwapIQ  bool                     // exchange I and Q
	Debug   bool                     // log the dialogue
	Notice  func(level, text string) // messages for the log (may be nil)
	Timeout time.Duration            // for each wait on the server (0 = 5 s)
}

type info struct {
	device, serial, maxRate, maxBW, stages, gainStages, maxGain, minFreq, maxFreq, resolution, minDecim, forcedFmt uint32
}

type sync_ struct {
	canControl, gain, devCentre, iqCentre, fftCentre, minIQ, maxIQ, minFFT, maxFFT uint32
}

// Source is an open SpyServer stream; it implements iq.Source.
type Source struct {
	o Options

	mu           sync.Mutex
	c            net.Conn
	reconnecting bool
	armed        bool // the stream is running: changes made by others matter
	lo           float64
	lo0          float64
	syncN        int // number of client syncs seen (under mu)
	wake         chan struct{}
	last         sync_
	info         info
	stage        int
	rate         float64
	format       int
	desc         string

	data  chan []complex64
	cur   []complex64
	done  chan struct{}
	once  sync.Once
	err   error
	lastW time.Time
	drops int
	lost  uint32
	lastL time.Time
}

func (s *Source) say(level, text string) {
	if s.o.Notice != nil {
		s.o.Notice(level, text)
	}
}

func (s *Source) timeout() time.Duration {
	if s.o.Timeout > 0 {
		return s.o.Timeout
	}
	return 5 * time.Second
}

// Open connects, tunes and starts the I/Q stream. Cancelling ctx stops waiting.
func Open(ctx context.Context, o Options) (*Source, error) {
	if o.Addr == "" {
		o.Addr = DefaultAddr
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", o.Addr)
	if err != nil {
		return nil, fmt.Errorf("SpyServer at %s: %w (is SpyServer running, and is port 5555 allowed by the firewall?)", o.Addr, err)
	}
	s := &Source{o: o, c: c, wake: make(chan struct{}), data: make(chan []complex64, 200), done: make(chan struct{})}
	go func() { // cancelling ctx during set-up or later ends the stream
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.done:
		}
	}()
	if err := s.setup(ctx, c); err != nil {
		s.Close()
		return nil, err
	}
	s.mu.Lock()
	s.lo0 = s.lo
	s.mu.Unlock()
	return s, nil
}

// ---- wire ----

func (s *Source) conn() net.Conn { s.mu.Lock(); defer s.mu.Unlock(); return s.c }

func (s *Source) send(c net.Conn, typ uint32, body []byte) error {
	b := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(b, typ)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	copy(b[8:], body)
	_, err := c.Write(b)
	return err
}

func (s *Source) set(c net.Conn, id, val uint32) error {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[:], id)
	binary.LittleEndian.PutUint32(b[4:], val)
	if s.o.Debug {
		s.say("info", fmt.Sprintf("SpyServer <- set setting %d = %d", id, val))
	}
	return s.send(c, cmdSetSetting, b[:])
}

func u32s(b []byte, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		if 4*i+4 <= len(b) {
			out[i] = binary.LittleEndian.Uint32(b[4*i:])
		}
	}
	return out
}

// ---- set-up ----

func (s *Source) setup(ctx context.Context, c net.Conn) error {
	name := []byte("navtexd")
	body := make([]byte, 4+len(name))
	binary.LittleEndian.PutUint32(body, protocolVersion)
	copy(body[4:], name)
	if err := s.send(c, cmdHello, body); err != nil {
		return err
	}
	br := bufio.NewReaderSize(c, 1<<16)
	// device information and the first client sync come first, before any stream
	var inf *info
	var syn *sync_
	c.SetReadDeadline(time.Now().Add(s.timeout()))
	for inf == nil || syn == nil {
		typ, _, _, b, err := readMsg(br)
		if err != nil {
			c.SetReadDeadline(time.Time{})
			return fmt.Errorf("SpyServer at %s did not answer the hello (%w); is this a SpyServer port, and is its version compatible?", s.o.Addr, err)
		}
		switch typ {
		case msgDeviceInfo:
			v := u32s(b, 12)
			inf = &info{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8], v[9], v[10], v[11]}
		case msgClientSync:
			v := u32s(b, 9)
			syn = &sync_{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8]}
		}
	}
	c.SetReadDeadline(time.Time{})
	s.mu.Lock()
	s.info, s.last = *inf, *syn
	s.mu.Unlock()
	if inf.maxRate == 0 {
		return errors.New("SpyServer reports no sample rate: is a receiver attached to it?")
	}
	if syn.canControl == 0 {
		return errors.New("SpyServer does not let this client control the receiver (it may need a password, or another client is controlling it)")
	}
	// the decimation stage whose rate is nearest to the target
	best, bestd := -1, math.MaxFloat64
	for st := int(inf.minDecim); st <= int(inf.stages); st++ {
		r := float64(inf.maxRate) / math.Pow(2, float64(st))
		if r < 100000 {
			break
		}
		if d := math.Abs(r - TargetRateHz); d < bestd {
			best, bestd = st, d
		}
	}
	if best < 0 {
		return fmt.Errorf("SpyServer's sample rate %d S/s is too low for NAVTEX", inf.maxRate)
	}
	s.stage = best
	s.rate = float64(inf.maxRate) / math.Pow(2, float64(best))
	s.format = fmtInt16
	switch inf.forcedFmt {
	case fmtUint8, fmtInt16, fmtFloat:
		s.format = int(inf.forcedFmt)
	}
	dev := map[uint32]string{1: "Airspy One", 2: "Airspy HF+", 3: "RTL-SDR"}[inf.device]
	if dev == "" {
		dev = "receiver"
	}
	s.desc = fmt.Sprintf("SpyServer at %s, %s %d", s.o.Addr, dev, inf.serial)

	want := uint32(math.Round(s.o.LOHz))
	if want < syn.minIQ || want > syn.maxIQ {
		return fmt.Errorf("SpyServer cannot tune the I/Q stream to %d Hz: it offers %d to %d Hz", want, syn.minIQ, syn.maxIQ)
	}
	if s.o.Gain >= 0 && uint32(s.o.Gain) > inf.maxGain {
		return fmt.Errorf("gain index %d is outside the range 0..%d of this receiver", s.o.Gain, inf.maxGain)
	}
	go s.reader(c, br)
	steps := []struct{ id, v uint32 }{{setIQFormat, uint32(s.format)}, {setIQDecimation, uint32(s.stage)}, {setIQFrequency, want}}
	if s.o.Gain >= 0 {
		steps = append(steps, struct{ id, v uint32 }{setGain, uint32(s.o.Gain)})
	}
	for _, st := range steps {
		if err := s.set(c, st.id, st.v); err != nil {
			return err
		}
	}
	// the server confirms the I/Q centre in a client sync
	if err := s.waitCentre(ctx, want); err != nil {
		return err
	}
	s.mu.Lock()
	s.lo = float64(s.last.iqCentre)
	s.armed = true
	s.mu.Unlock()
	if err := s.set(c, setStreamingMode, streamIQOnly); err != nil {
		return err
	}
	return s.set(c, setStreamingEnabled, 1)
}

// waitCentre waits until a client sync reports the wanted I/Q centre frequency.
func (s *Source) waitCentre(ctx context.Context, want uint32) error {
	deadline := time.After(s.timeout())
	for {
		s.mu.Lock()
		got, wake := s.last.iqCentre, s.wake
		s.mu.Unlock()
		if got == want {
			return nil
		}
		select {
		case <-wake:
		case <-deadline:
			return fmt.Errorf("SpyServer did not accept the I/Q centre frequency %d Hz (it says %d)", want, got)
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return s.Err()
		}
	}
}

func readMsg(br *bufio.Reader) (typ, stream, seq uint32, body []byte, err error) {
	var h [20]byte
	if _, err = io.ReadFull(br, h[:]); err != nil {
		return
	}
	typ = binary.LittleEndian.Uint32(h[4:]) & 0xFFFF
	stream = binary.LittleEndian.Uint32(h[8:])
	seq = binary.LittleEndian.Uint32(h[12:])
	n := binary.LittleEndian.Uint32(h[16:])
	if n > maxBody {
		err = fmt.Errorf("message of %d bytes (limit %d): not a SpyServer?", n, maxBody)
		return
	}
	body = make([]byte, n)
	_, err = io.ReadFull(br, body)
	return
}

// ---- stream ----

func (s *Source) reader(c net.Conn, br *bufio.Reader) {
	var last uint32
	have := false
	for {
		typ, _, seq, b, err := readMsg(br)
		if err != nil {
			select {
			case <-s.done: // closed by us
			default:
				c.Close()
				s.mu.Lock()
				stale := s.c != c || s.reconnecting
				if !stale {
					s.armed = false
					s.reconnecting = true
				}
				s.mu.Unlock()
				if stale {
					return
				}
				s.say("warn", fmt.Sprintf("SpyServer closed the connection (%v); trying to reconnect every %s", err, retryEvery))
				go s.reconnect()
			}
			return
		}
		switch typ {
		case msgClientSync:
			v := u32s(b, 9)
			sy := sync_{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8]}
			if s.o.Debug {
				s.say("info", fmt.Sprintf("SpyServer says: client sync, I/Q centre %d, device centre %d, can control %d, gain %d", sy.iqCentre, sy.devCentre, sy.canControl, sy.gain))
			}
			s.mu.Lock()
			prev := s.last.iqCentre
			s.last = sy
			s.syncN++
			armed, lo := s.armed, s.lo
			close(s.wake)
			s.wake = make(chan struct{})
			s.mu.Unlock()
			if armed && sy.iqCentre != prev && float64(sy.iqCentre) != lo {
				s.finish(centreErr(float64(sy.iqCentre), lo))
			}
		case msgUint8IQ, msgInt16IQ, msgFloatIQ:
			if have && seq != last+1 {
				s.lost += seq - last - 1
				if time.Since(s.lastL) > 10*time.Second {
					s.lastL = time.Now()
					s.say("warn", fmt.Sprintf("SpyServer input: %d blocks of samples were lost on the way (sequence gap)", s.lost))
					s.lost = 0
				}
			}
			last, have = seq, true
			s.samples(typ, b)
		}
	}
}

func (s *Source) samples(typ uint32, b []byte) {
	var out []complex64
	switch typ {
	case msgInt16IQ:
		n := len(b) / 4
		out = make([]complex64, n)
		for i := range out {
			a := float32(int16(binary.LittleEndian.Uint16(b[4*i:]))) / 32768
			q := float32(int16(binary.LittleEndian.Uint16(b[4*i+2:]))) / 32768
			out[i] = complex(a, q)
		}
	case msgUint8IQ:
		n := len(b) / 2
		out = make([]complex64, n)
		for i := range out {
			out[i] = complex((float32(b[2*i])-127.5)/127.5, (float32(b[2*i+1])-127.5)/127.5)
		}
	case msgFloatIQ:
		n := len(b) / 8
		out = make([]complex64, n)
		for i := range out {
			out[i] = complex(math.Float32frombits(binary.LittleEndian.Uint32(b[8*i:])), math.Float32frombits(binary.LittleEndian.Uint32(b[8*i+4:])))
		}
	}
	if len(out) == 0 {
		return
	}
	if s.o.SwapIQ {
		for i, v := range out {
			out[i] = complex(imag(v), real(v))
		}
	}
	select {
	case s.data <- out:
	default: // the decoder is not keeping up: drop rather than stall the connection
		s.drops++
		if time.Since(s.lastW) > 10*time.Second {
			s.lastW = time.Now()
			s.say("warn", fmt.Sprintf("SpyServer input: the decoder is too slow, %d blocks of samples dropped", s.drops))
			s.drops = 0
		}
	}
}

// centreErr explains the stop after the I/Q centre frequency changed without this program asking for it.
func centreErr(now, was float64) error {
	return fmt.Errorf("the SpyServer's I/Q centre frequency was changed from %.0f Hz to %.0f Hz, not by this program; "+
		"the NAVTEX carrier is no longer where this channel expects it, so the channel has stopped instead of decoding wrongly. Start the channel again", now, was)
}

// reconnect runs after the connection was lost: it dials and sets the stream up again until that works or the source is
// closed. If the I/Q centre is not the one the channel started with, the stream ends with an error.
func (s *Source) reconnect() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() { s.mu.Lock(); s.reconnecting = false; s.mu.Unlock() }()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryEvery):
		}
		var d net.Dialer
		dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
		c, err := d.DialContext(dctx, "tcp", s.o.Addr)
		dcancel()
		if err == nil {
			s.mu.Lock()
			s.c = c
			s.armed = false
			s.last = sync_{}
			s.mu.Unlock()
			err = s.setup(ctx, c)
			if err == nil {
				s.mu.Lock()
				lo, lo0 := s.lo, s.lo0
				s.mu.Unlock()
				if lo != lo0 {
					s.finish(centreErr(lo, lo0))
					return
				}
				s.say("info", "SpyServer is back; the stream resumed")
				return
			}
			c.Close()
			s.mu.Lock()
			s.armed = false
			s.mu.Unlock()
		}
		if e := err.Error(); e != last && ctx.Err() == nil {
			last = e
			s.say("warn", fmt.Sprintf("SpyServer is not available (%v); retrying every %s", err, retryEvery))
		}
	}
}

// ---- iq.Source ----

func (s *Source) SampleRate() float64 { return s.rate }

// CentreHz is the centre frequency of the I/Q stream as the server reports it.
func (s *Source) CentreHz() float64 { s.mu.Lock(); defer s.mu.Unlock(); return s.lo }
func (s *Source) Kind() iq.Kind     { return iq.IQ }

// Describe is a one-line description of what was connected to.
func (s *Source) Describe() string { return s.desc }

// Err is why the stream ended, or io.EOF.
func (s *Source) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return io.EOF
}

func (s *Source) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

// Read returns samples; it blocks until some arrive, and fails when the stream ends.
func (s *Source) Read(dst []complex64) (int, error) {
	for len(s.cur) == 0 {
		select {
		case b := <-s.data:
			s.cur = b
		case <-s.done:
			select {
			case b := <-s.data:
				s.cur = b
			default:
				return 0, s.Err()
			}
		}
	}
	n := copy(dst, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// Close stops the stream and closes the connection.
func (s *Source) Close() error {
	s.once.Do(func() { close(s.done) })
	c := s.conn()
	s.set(c, setStreamingEnabled, 0)
	return c.Close()
}
