// Package sdrconnect takes I/Q samples from SDRplay's SDRconnect program (also its headless version) over its WebSocket
// API: it selects the receiver, sets the sample rate, the centre frequency (LO), antenna and LNA state, starts the I/Q
// stream and delivers the samples as an iq.Source, decimated to about 250 kHz. The API is described in SDRplay's
// "SDRconnect WebSocket API" document; see docs/SDRCONNECT_DESIGN.md for what this program uses of it and what was
// found out by trying.
package sdrconnect

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"navtex/internal/ddc"
	"navtex/internal/iq"
	"navtex/internal/ws"
)

// DefaultAddr is where SDRconnect's WebSocket server listens (Preferences, WebSocket Server).
const DefaultAddr = "127.0.0.1:5454"

// OutRateHz is the rate of the samples this source delivers: the hardware rate is decimated to it.
const OutRateHz = 250000

// Rates are the hardware sample rates that can be asked for (SDRconnect also offers 62.5 and 125 kHz, whose streams
// arrive about 3% short, and 6 to 10 MHz, which cost a lot of processing for nothing).
var Rates = []float64{250000, 500000, 1000000, 2000000}

// Options says what to ask SDRconnect for.
type Options struct {
	Addr     string  // host:port of the WebSocket server
	Device   string  // part of the receiver's name in SDRconnect's list; empty = the one SDRconnect has selected
	RateHz   float64 // hardware sample rate: one of Rates
	LOHz     float64 // centre frequency (the receiver's LO), Hz
	DialHz   float64 // frequency of the "VFO" (the channel frequency); it must lie inside the received span
	Antenna  string  // antenna / input name as in SDRconnect's list; empty = leave as it is
	LNAState int     // -1 = leave as it is
	SwapIQ   bool
	Debug    bool                     // log every message exchanged (except the frequent signal readings)
	Notice   func(level, text string) // messages for the log (may be nil)
}

type msg struct {
	EventType string `json:"event_type"`
	Property  string `json:"property"`
	Value     any    `json:"value"`
	Device    string `json:"device,omitempty"`
}

// Source is an open SDRconnect I/Q stream. It implements iq.Source.
type Source struct {
	o    Options
	c    *ws.Conn
	rate float64 // delivered rate
	lo   float64 // centre frequency as SDRconnect reports it
	info string  // receiver and version, for the log

	mu    sync.Mutex
	props map[string]string
	gen   map[string]int
	wake  chan struct{} // closed and replaced on every update of props

	stages     []*ddc.Decimator
	data       chan []complex64
	cur        []complex64
	done       chan struct{}
	once       sync.Once
	err        error // why the stream ended (read under mu)
	drops      int
	lastW      time.Time
	startedDev bool
	armed      bool    // the stream is running: changes made by others matter (under mu)
	lo0        float64 // the centre frequency the channel was set up with
	radioOff   bool    // SDRconnect reported started = false (under mu)
}

func (s *Source) conn() *ws.Conn { s.mu.Lock(); defer s.mu.Unlock(); return s.c }

func (s *Source) say(level, text string) {
	if s.o.Notice != nil {
		s.o.Notice(level, text)
	}
}

// Open connects, sets up the receiver and starts the I/Q stream. Cancelling ctx stops waiting.
func Open(ctx context.Context, o Options) (*Source, error) {
	if o.Addr == "" {
		o.Addr = DefaultAddr
	}
	okRate := false
	for _, r := range Rates {
		okRate = okRate || r == o.RateHz
	}
	if !okRate {
		return nil, fmt.Errorf("sample rate %.0f Hz: want one of 250000, 500000, 1000000, 2000000", o.RateHz)
	}
	c, err := ws.Dial(ctx, o.Addr, "/")
	if err != nil {
		return nil, fmt.Errorf("SDRconnect at %s: %w (is SDRconnect running, with its WebSocket server switched on under Preferences, and is port 5454 allowed by the firewall?)", o.Addr, err)
	}
	q := int(o.RateHz / OutRateHz)
	s := &Source{o: o, c: c, rate: o.RateHz / float64(q), props: map[string]string{}, gen: map[string]int{}, wake: make(chan struct{}),
		data: make(chan []complex64, 200), done: make(chan struct{})}
	f := o.RateHz
	for _, k := range ddc.StageFactors(q) {
		s.stages = append(s.stages, ddc.NewDecimator(k, ddc.DesignDecimator(k, f)))
		f /= float64(k)
	}
	go s.reader(c)
	go func() { // cancelling ctx during setup or later ends the stream
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.done:
		}
	}()
	if err := s.setup(ctx); err != nil {
		s.Close()
		return nil, err
	}
	s.mu.Lock()
	s.lo0 = s.lo
	s.mu.Unlock()
	return s, nil
}

// ---- messages from SDRconnect ----

func (s *Source) reader(c *ws.Conn) {
	var cbuf []complex128
	for {
		typ, p, err := c.Read()
		if err != nil {
			select {
			case <-s.done: // closed by us
			default:
				c.Close()
				s.mu.Lock()
				s.armed = false
				s.mu.Unlock()
				s.say("warn", fmt.Sprintf("SDRconnect closed the connection (%v); trying to reconnect every %s", err, retryEvery))
				go s.reconnect()
			}
			return
		}
		switch typ {
		case ws.Text:
			s.text(p)
		case ws.Binary:
			if len(p) < 2 || binary.LittleEndian.Uint16(p) != 2 { // type 2 = I/Q of the primary receiver
				continue
			}
			p = p[2:]
			n := len(p) / 4
			if cap(cbuf) < n {
				cbuf = make([]complex128, n)
			}
			cbuf = cbuf[:n]
			for i := range cbuf {
				a := float64(int16(binary.LittleEndian.Uint16(p[4*i:]))) / 32768
				b := float64(int16(binary.LittleEndian.Uint16(p[4*i+2:]))) / 32768
				if s.o.SwapIQ {
					a, b = b, a
				}
				cbuf[i] = complex(a, b)
			}
			y := cbuf
			for _, st := range s.stages {
				y = st.Process(y)
			}
			if len(y) == 0 {
				continue
			}
			out := make([]complex64, len(y))
			for i, v := range y {
				out[i] = complex64(v)
			}
			select {
			case s.data <- out:
			default: // the decoder is not keeping up: drop rather than stall SDRconnect
				s.drops++
				if time.Since(s.lastW) > 10*time.Second {
					s.lastW = time.Now()
					s.say("warn", fmt.Sprintf("SDRconnect input: the decoder is too slow, %d blocks of samples dropped", s.drops))
					s.drops = 0
				}
			}
		}
	}
}

func (s *Source) text(p []byte) {
	var m struct {
		EventType string `json:"event_type"`
		Property  string `json:"property"`
		Value     any    `json:"value"`
	}
	if json.Unmarshal(p, &m) != nil || m.Property == "" {
		return
	}
	if m.EventType != "get_property_response" && m.EventType != "property_changed" {
		return
	}
	v := fmt.Sprint(m.Value)
	if f, ok := m.Value.(float64); ok {
		v = strconv.FormatFloat(f, 'f', -1, 64)
	}
	if s.o.Debug && m.Property != "signal_power" && m.Property != "signal_snr" && m.Property != "overload" {
		s.say("info", fmt.Sprintf("SDRconnect says: %s %s = %s", m.EventType, m.Property, v))
	}
	s.mu.Lock()
	old, had := s.props[m.Property]
	s.props[m.Property] = v
	s.gen[m.Property]++
	close(s.wake)
	s.wake = make(chan struct{})
	lo := s.lo
	started := s.armed // the stream is running: later changes by others matter
	s.mu.Unlock()
	if m.EventType == "property_changed" && started && m.Property == "started" && had && old != v {
		on := strings.EqualFold(strings.TrimSpace(v), "true")
		s.mu.Lock()
		s.radioOff = !on
		s.mu.Unlock()
		if on {
			s.say("info", "SDRconnect: the radio is running again; samples resume")
		} else {
			s.say("warn", "SDRconnect: the radio was stopped (in SDRconnect); no samples until it is started again, the channel waits")
		}
	}
	if m.EventType == "property_changed" && started && had && old != v {
		switch m.Property {
		case "device_sample_rate":
			if num(v) != s.o.RateHz {
				s.finish(fmt.Errorf("SDRconnect's sample rate was changed to %s Hz by someone else; the stream no longer matches", v))
			}
		case "device_center_frequency":
			if num(v) != lo {
				s.finish(centreErr(num(v), lo))
			}
		}
	}
}

// centreErr explains the stop after someone else has moved SDRconnect's centre frequency.
func centreErr(now, was float64) error {
	return fmt.Errorf("SDRconnect's centre frequency (LO) was changed from %.0f Hz to %.0f Hz by someone else, probably by retuning the receiver in SDRconnect by more than about 1 MHz; SDRconnect then moves its centre. "+
		"The NAVTEX carrier is no longer where this channel expects it, so the channel has stopped instead of decoding wrongly. Tune SDRconnect back near the channel frequency and start the channel again", now, was)
}

var retryEvery = 5 * time.Second

// reconnect runs after the connection was lost: it dials and sets the receiver up again until it works or the source is
// closed. If SDRconnect's centre frequency is not the one the channel was set up with, the stream ends with an error.
func (s *Source) reconnect() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
		c, err := ws.Dial(ctx, s.o.Addr, "/")
		if err == nil {
			s.mu.Lock()
			s.c = c
			s.props = map[string]string{}
			s.armed = false
			s.mu.Unlock()
			go s.reader(c)
			sctx, scancel := context.WithTimeout(ctx, 60*time.Second)
			err = s.setup(sctx)
			scancel()
			if err == nil {
				s.mu.Lock()
				lo, lo0 := s.lo, s.lo0
				s.mu.Unlock()
				if lo != lo0 {
					s.finish(centreErr(lo, lo0))
					return
				}
				s.say("info", "SDRconnect is back; the stream resumed")
				return
			}
			c.Close()
			s.mu.Lock()
			s.armed = false
			s.mu.Unlock()
		}
		if e := err.Error(); e != last && ctx.Err() == nil {
			last = e
			s.say("warn", fmt.Sprintf("SDRconnect is not available (%v); retrying every %s", err, retryEvery))
		}
	}
}

func num(v string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64); return f }

func (s *Source) send(m msg) error {
	b, _ := json.Marshal(m)
	if s.o.Debug {
		s.say("info", "SDRconnect <- "+string(b))
	}
	return s.conn().WriteText(string(b))
}

// get asks SDRconnect for a property and waits for the answer.
func (s *Source) get(ctx context.Context, prop string) (string, error) {
	s.mu.Lock()
	g := s.gen[prop]
	s.mu.Unlock()
	if err := s.send(msg{EventType: "get_property", Property: prop, Value: "N/A"}); err != nil {
		return "", err
	}
	return s.wait(ctx, prop, func(v string, gen int) bool { return gen > g }, 3*time.Second)
}

// wait waits until the property satisfies ok (it sees the value and how many times it was updated).
func (s *Source) wait(ctx context.Context, prop string, ok func(v string, gen int) bool, d time.Duration) (string, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		s.mu.Lock()
		v, have := s.props[prop]
		gen, wake := s.gen[prop], s.wake
		s.mu.Unlock()
		if have && ok(v, gen) {
			return v, nil
		}
		select {
		case <-wake:
		case <-t.C:
			return v, fmt.Errorf("SDRconnect did not answer for %q", prop)
		case <-s.done:
			return "", s.Err()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// set sets a property and waits until SDRconnect reports the wanted value (read back by asking).
func (s *Source) set(ctx context.Context, prop, value string, same func(got string) bool) error {
	return s.setWait(ctx, prop, value, same, 4*time.Second)
}

func (s *Source) setWait(ctx context.Context, prop, value string, same func(got string) bool, wait time.Duration) error {
	if err := s.send(msg{EventType: "set_property", Property: prop, Value: value}); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	var got string
	for time.Now().Before(deadline) {
		select {
		case <-time.After(250 * time.Millisecond):
		case <-s.done:
			return s.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
		v, err := s.get(ctx, prop)
		if err != nil {
			return err
		}
		if got = v; same(v) {
			return nil
		}
	}
	return fmt.Errorf("SDRconnect did not accept %s = %s (it says %s)", prop, value, got)
}

func sameNum(want float64) func(string) bool {
	return func(v string) bool { return math.Abs(num(v)-want) < 0.5 }
}

// ---- set-up ----

func (s *Source) setup(ctx context.Context) error {
	ver, err := s.get(ctx, "api_version")
	if err != nil {
		return fmt.Errorf("SDRconnect does not answer the API (%w)", err)
	}
	idx, err := s.selectDevice(ctx)
	if err != nil {
		return err
	}
	// (the headless SDRconnect says can_control is false until a receiver has been opened by a selection, even if it
	// reports one as active, so this is asked only now)
	s.needControl(ctx, idx)
	dev, _ := s.get(ctx, "active_device")
	s.info = fmt.Sprintf("SDRconnect at %s (API %s), receiver %s", s.o.Addr, ver, strings.TrimSpace(dev))

	// quiet the streams that are not wanted
	for _, ev := range []string{"spectrum_enable", "audio_stream_enable"} {
		s.send(msg{EventType: ev, Property: "", Value: "false"})
	}
	if a := strings.TrimSpace(s.o.Antenna); a != "" {
		valid, _ := s.get(ctx, "valid_antennas")
		name, err := matchName(strings.Split(valid, ","), a)
		if err != nil {
			return fmt.Errorf("antenna %q: %w (SDRconnect offers: %s)", a, err, strings.TrimSpace(valid))
		}
		if err := s.set(ctx, "active_antenna", name, func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), name) }); err != nil {
			return err
		}
	}
	if s.o.LNAState >= 0 {
		lo, _ := s.get(ctx, "lna_state_min")
		hi, _ := s.get(ctx, "lna_state_max")
		if l := float64(s.o.LNAState); l < num(lo) || l > num(hi) {
			return fmt.Errorf("lna_state %d is outside the range %s..%s of this receiver", s.o.LNAState, lo, hi)
		}
		if err := s.set(ctx, "lna_state", strconv.Itoa(s.o.LNAState), sameNum(float64(s.o.LNAState))); err != nil {
			return err
		}
	}
	if err := s.set(ctx, "device_sample_rate", strconv.FormatInt(int64(s.o.RateHz), 10), sameNum(s.o.RateHz)); err != nil {
		return err
	}
	wantLO := math.Round(s.o.LOHz)
	if err := s.setWait(ctx, "device_center_frequency", strconv.FormatInt(int64(wantLO), 10), sameNum(wantLO), 1500*time.Millisecond); err != nil {
		// SDRconnect 1.0.10 ignores writes of the centre frequency. It re-centres by itself when the tuned frequency
		// (VFO) leaves the received span, and at 250 kS/s it then puts the centre exactly on the VFO (at higher rates it
		// adds an offset of its own). So the VFO is moved away from the span and then onto the wanted centre.
		s.recentre(ctx, wantLO)
		cur, gerr := s.get(ctx, "device_center_frequency")
		if gerr != nil || math.Abs(s.o.DialHz-num(cur)) > 0.4*OutRateHz {
			return err
		}
		if math.Abs(num(cur)-wantLO) > 1000 {
			s.say("warn", fmt.Sprintf("SDRconnect would not move its centre frequency (it stays at %.0f Hz instead of %.0f Hz); using it as it is, the carrier is expected %.0f Hz from it", num(cur), wantLO, s.o.DialHz-num(cur)))
		}
	}
	if s.o.DialHz > 0 { // keep the VFO inside the received span, so that SDRconnect does not move the centre to follow it
		s.set(ctx, "device_vfo_frequency", strconv.FormatInt(int64(math.Round(s.o.DialHz)), 10), sameNum(math.Round(s.o.DialHz)))
	}
	cf, err := s.get(ctx, "device_center_frequency")
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.lo = num(cf)
	s.armed = true
	s.mu.Unlock()
	if st, _ := s.get(ctx, "started"); !strings.EqualFold(strings.TrimSpace(st), "true") {
		if err := s.set(ctx, "device_stream_enable", "true", func(string) bool { return true }); err != nil {
			return err
		}
		s.startedDev = true
		time.Sleep(500 * time.Millisecond)
	}
	return s.send(msg{EventType: "iq_stream_enable", Property: "", Value: "true"})
}

// recentre makes SDRconnect move its centre frequency to want by way of the VFO: tuned well outside the received span
// (so that SDRconnect has to re-centre), then onto want. The result is read back by the caller.
func (s *Source) recentre(ctx context.Context, want float64) {
	far := want + 2*s.o.RateHz + 500000
	if far > 1.9e9 {
		far = want - 2*s.o.RateHz - 500000
	}
	if far < 1000 {
		return
	}
	s.say("info", fmt.Sprintf("SDRconnect: re-centring through the tuned frequency (%.0f Hz, then %.0f Hz)", far, want))
	s.setWait(ctx, "device_vfo_frequency", strconv.FormatInt(int64(far), 10), sameNum(far), 2*time.Second)
	time.Sleep(500 * time.Millisecond)
	s.setWait(ctx, "device_vfo_frequency", strconv.FormatInt(int64(want), 10), sameNum(want), 2*time.Second)
	time.Sleep(500 * time.Millisecond)
}

// needControl makes sure the receiver is open for this client. If can_control is false the receiver is selected
// explicitly (the index base is not documented, so idx and idx+1 are tried); if it stays false the set-up goes on, and
// the first setting that is refused says what is wrong.
func (s *Source) needControl(ctx context.Context, idx int) {
	can := func() bool {
		v, err := s.get(ctx, "can_control")
		return err != nil || !strings.EqualFold(strings.TrimSpace(v), "false") // true, or not reported
	}
	if can() {
		return
	}
	if idx >= 0 {
		for _, k := range []int{idx, idx + 1} {
			s.send(msg{EventType: "selected_device", Property: "", Value: strconv.Itoa(k)})
			for i := 0; i < 5; i++ {
				select {
				case <-time.After(400 * time.Millisecond):
				case <-ctx.Done():
					return
				}
				if can() {
					return
				}
			}
		}
	}
	s.say("warn", "SDRconnect reports can_control = false; trying to set up the receiver anyway")
}

// matchName finds the entry of list that the user means: the same name, ignoring case, spaces and hyphens, or else the
// only entry containing it.
func matchName(list []string, want string) (string, error) {
	norm := func(x string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '-' || r == '_' {
				return -1
			}
			return r
		}, strings.ToLower(x))
	}
	w := norm(want)
	var hits []string
	for _, x := range list {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if norm(x) == w {
			return x, nil
		}
		if strings.Contains(norm(x), w) {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", errors.New("not in the list")
	}
	return "", fmt.Errorf("fits several (%s)", strings.Join(hits, ", "))
}

// selectDevice makes the wanted receiver the active one. The API's device index is not documented to start at 0 or 1,
// so both are tried and the answer (active_device) decides.
func (s *Source) selectDevice(ctx context.Context) (int, error) {
	list, err := s.get(ctx, "valid_devices")
	if err != nil {
		return -1, err
	}
	active, _ := s.get(ctx, "active_device")
	var names []string
	for _, x := range strings.Split(list, ",") {
		if x = strings.TrimSpace(x); x != "" {
			names = append(names, x)
		}
	}
	want := strings.ToLower(strings.TrimSpace(s.o.Device))
	if len(names) == 0 && strings.TrimSpace(active) == "" {
		return -1, errors.New("SDRconnect lists no receiver: is the RSP connected, and is it free (not in use by another program)?")
	}
	if want == "" && strings.TrimSpace(active) != "" {
		return -1, nil
	}
	idx := -1
	for i, n := range names {
		if (want == "" && idx < 0) || (want != "" && strings.Contains(strings.ToLower(n), want)) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return -1, fmt.Errorf("no receiver matching %q in SDRconnect's list (%s)", s.o.Device, list)
	}
	target := strings.ToLower(names[idx])
	matches := func(a string) bool {
		a = strings.ToLower(strings.TrimSpace(a))
		return a != "" && (a == target || strings.Contains(a, target) || strings.Contains(target, a))
	}
	if matches(active) {
		return idx, nil
	}
	for _, k := range []int{idx, idx + 1} {
		s.send(msg{EventType: "selected_device", Property: "", Value: strconv.Itoa(k)})
		time.Sleep(700 * time.Millisecond)
		if active, _ = s.get(ctx, "active_device"); matches(active) {
			return idx, nil
		}
	}
	return -1, fmt.Errorf("could not select %q in SDRconnect (active receiver: %s)", names[idx], active)
}

// ---- iq.Source ----

func (s *Source) SampleRate() float64 { return s.rate }

// CentreHz is the receiver's centre frequency (LO) as SDRconnect reports it.
func (s *Source) CentreHz() float64 { s.mu.Lock(); defer s.mu.Unlock(); return s.lo }
func (s *Source) Kind() iq.Kind     { return iq.IQ }

// Describe is a one-line description of what was connected to.
func (s *Source) Describe() string { return s.info }

// Err is why the stream ended, or nil.
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

// Read returns decimated samples; it blocks until some arrive, and fails when the stream ends.
func (s *Source) Read(dst []complex64) (int, error) {
	for len(s.cur) == 0 {
		select {
		case b := <-s.data:
			s.cur = b
		case <-s.done:
			select { // samples that arrived before the end are still delivered
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

// Close stops the I/Q stream (and the receiver, if this program started it) and closes the connection.
func (s *Source) Close() error {
	s.once.Do(func() { close(s.done) }) // (no error: closed on purpose)
	s.send(msg{EventType: "iq_stream_enable", Property: "", Value: "false"})
	if s.startedDev {
		s.send(msg{EventType: "set_property", Property: "device_stream_enable", Value: "false"})
	}
	return s.conn().Close()
}
