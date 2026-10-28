// Package engine is the live decoding core: sample blocks in; decoded text, records, meters and status out. It also
// accepts run-time controls (resync, autotune, polarity, unshift-on-space). An Engine is not safe for concurrent use:
// call its methods from the goroutine that feeds it (a server serialises controls through that goroutine).
package engine

import (
	"errors"
	"fmt"
	"math"

	"navtex/internal/config"
	"navtex/internal/ddc"
	"navtex/internal/fsk"
	"navtex/internal/iq"
	"navtex/internal/segment"
	"navtex/internal/sitor"
)

// SyncState is the framing state shown by the SYNC indicator.
type SyncState int

const (
	SyncLost SyncState = iota // no symbol timing
	SyncPre                   // a signal with symbol timing is present, word framing not yet locked
	SyncFull                  // word framing locked
)

func (s SyncState) String() string { return [...]string{"lost", "pre-sync", "sync"}[s] }

// Level describes the input signal level since the previous report (0..1 of full scale).
type Level struct {
	Peak, RMS float64
	Clip      bool
}

// Spectrum is one frame of the input spectrum in dB. Bin i is at StartHz + i*StepHz (audio Hz for real input; Hz from
// the receiver centre for I/Q input). CarrierHz is the tracked carrier on the same axis.
type Spectrum struct {
	StartHz, StepHz float64
	Filtered, Raw   []float32
	Channel         []float32 // same grid: the signal after the channel filter, chanOff (-300) where it has no data; nil if off
	CarrierHz       float64
	FullScaleDB     float64 // level of a full-scale tone in the units of the spectrum
}

// Carrier describes the tuning.
type Carrier struct {
	Have     bool    // a carrier is detected (or fixed by hand)
	Autotune bool    // automatic search and tracking on
	OffsetHz float64 // carrier offset from the channel frequency as seen at the down-converter centre
	AudioHz  float64 // carrier frequency on the input axis (audio Hz for real input)
}

// Meters is a periodic report.
type Meters struct {
	StreamS  float64
	Level    Level
	Quality  float64 // 0..100, latest value
	Sync     SyncState
	Data     bool // characters were decoded within the last two seconds
	Carrier  Carrier
	Spectrum *Spectrum // nil unless a new frame is available
}

// Handlers receive the engine's output; any may be nil.
type Handlers struct {
	Text   func(text string, streamS float64) // decoded characters with their send time
	Status func(text string)                  // carrier, framing and bit-order changes
	Meters func(Meters)
	Record func(segment.Record) // a finished message or fragment
	Line   func(segment.Line)   // every completed line of text (for live displays)
}

// Summary describes a finished run.
type Summary struct {
	Seconds       float64
	Samples       int64
	SymbolsLocked int64
	BlankedFrac   float64
	FinalF0       float64
	CarrierFound  bool
	Order         sitor.Order
	DecimatedRate float64
	DecimatedBy   int
	ShiftHz       float64
}

type qpoint struct{ t, q float64 }

// Engine is one decoding channel.
type Engine struct {
	scope *Scope
	cfg   config.Config
	h     Handlers
	fs    float64
	kind  iq.Kind
	shift float64
	baud  float64
	D     int

	front *ddc.DDC
	rx    *fsk.Receiver
	dec   sitor.Framer
	seg   *segment.Segmenter

	inSamples   int64
	lockedSyms  int64
	loggedF0    float64 // carrier offset in the last log line
	lastChanges int
	lastText    float64

	// level
	lvPeak, lvSum float64
	lvN           int
	lvClip        bool
	nextMeter     float64

	// quality
	qMax2, qSum2 float64
	qN           int
	qNeed        int
	qHist        []qpoint
	qLast        float64
	eye          [eyeN]float64 // soft values (Hz) of the last locked symbols, a ring
	eyeFill      int           // symbols written so far (saturating at eyeN)
	eyePos       int
	eyeLastT     float64 // stream time when the last locked symbol arrived
	qCrest, qEye float64 // the two components of the latest quality value (qEye < 0: not available)

	spec *specState
	chs  *chanSpec // spectrum after the channel filter
}

// New creates an engine for a source with the given sample rate, centre frequency and kind.
func New(fs, centreHz float64, kind iq.Kind, cfg config.Config, h Handlers) (*Engine, error) {
	if err := cfg.ValidateChannel(); err != nil {
		return nil, err
	}
	chHz := cfg.Channel.FrequencyHz
	shift := chHz - centreHz // the receiver's window is centered here
	nyq := fs / 2
	if kind == iq.Real {
		if shift < 300 || shift > nyq-300 {
			return nil, fmt.Errorf("channel %.1f kHz with LO %.1f kHz puts the carrier at %.0f Hz in the audio; "+
				"it must be between 300 Hz and %.0f Hz (sample rate %.0f Hz); for real audio set the centre/LO frequency to the receiver's dial frequency", chHz/1e3, centreHz/1e3, shift, nyq-300, fs)
		}
	} else if shift < -nyq+1500 || shift > nyq-1500 {
		return nil, fmt.Errorf("channel %.1f kHz is outside the received band: centre %.1f kHz, rate %.0f Hz (usable +-%.1f kHz), channel offset %+.1f kHz",
			chHz/1e3, centreHz/1e3, fs, (nyq-1500)/1e3, (chHz-centreHz)/1e3)
	}
	D := ddc.PickDecim(fs, cfg.DDC.TargetRateHz)
	if D == 0 {
		return nil, errors.New("no usable decimation factor")
	}
	e := &Engine{cfg: cfg, h: h, fs: fs, kind: kind, shift: shift, D: D, baud: cfg.Receiver.Baud}
	e.front = ddc.New(fs, shift, D)
	rcfg := cfg.Receiver
	rcfg.Params.Fs = e.front.OutputRate()
	e.rx = fsk.NewReceiver(rcfg)
	e.dec = sitor.New(cfg.Decoder)
	e.seg = segment.New(cfg.Segment, e.Quality, func(r segment.Record) {
		if e.h.Record != nil {
			e.h.Record(r)
		}
	})
	e.seg.OnLine = func(l segment.Line) {
		if e.h.Line != nil {
			e.h.Line(l)
		}
	}
	e.scope = NewScope(e.front.OutputRate(), cfg.Receiver.Baud, cfg.Receiver.ShiftHz, e.rx.Demod().FilterDelay())
	e.rx.Demod().OnBlock = func(n0 int64, x, y []complex128) {
		e.scope.Block(n0, x, y)
		if e.chs != nil {
			e.chs.feed(y)
		}
	}
	e.qNeed = int(cfg.Meters.QualityWindowS * e.front.OutputRate())
	e.lastText = -1e9
	e.spec = newSpec(fs, kind, e.viewCentre(cfg.Channel.ViewOffset), cfg.Meters)
	e.chs = newChanSpec(e.front.OutputRate(), e.spec.step, e.spec.n, cfg.Meters.SpectrumSmooth)
	if !cfg.Autotune() {
		e.rx.SetAutotune(false)
		e.rx.SetCarrier(cfg.ManualCarrierHz() - shift)
	}
	return e, nil
}

// viewCentre is the centre of the spectrum slice: the window centre moved by the view offset, kept inside the band.
func (e *Engine) viewCentre(off float64) float64 {
	v, nyq := e.shift+off, e.fs/2
	if e.kind == iq.Real {
		return math.Max(300, math.Min(nyq-300, v))
	}
	return math.Max(-nyq+1500, math.Min(nyq-1500, v))
}

// ViewHz is the frequency the displayed spectrum slice is centered on.
func (e *Engine) ViewHz() float64 { return e.viewCentre(e.cfg.Channel.ViewOffset) }

// ShiftHz is the frequency (relative to the source's center, or audio frequency) that the spectrum and the receiver are centered on.
func (e *Engine) ShiftHz() float64 { return e.shift }

// Scope returns the oscilloscope recorder of this engine.
func (e *Engine) Scope() *Scope { return e.scope }

// Now returns the stream time in seconds of the input consumed so far.
func (e *Engine) Now() float64 { return float64(e.inSamples) / e.fs }

// Process consumes a block of input samples.
func (e *Engine) Process(block []complex64) {
	if len(block) == 0 {
		return
	}
	e.measureLevel(block)
	e.spec.feed(block)
	e.inSamples += int64(len(block))
	e.handleSyms(e.rx.Process(e.front.Process(block)))
	e.measureQuality()
	e.reportCarrier()
	e.seg.Tick(e.Now())
	if now := e.Now(); now >= e.nextMeter {
		e.emitMeters()
		e.nextMeter = now + 1/e.cfg.Meters.RateHz
	}
}

// Flush ends the stream: releases pending symbols, text and records.
func (e *Engine) Flush() {
	e.handleSyms(e.rx.Flush())
	e.handleSitor(e.dec.Flush())
	e.reportCarrier()
	e.seg.Flush(e.Now())
}

// Summary returns the run totals.
func (e *Engine) Summary() Summary {
	return Summary{Seconds: e.Now(), Samples: e.inSamples, SymbolsLocked: e.lockedSyms, BlankedFrac: e.rx.Blanker().BlankedFraction(),
		FinalF0: e.rx.F0(), CarrierFound: e.rx.HaveCarrier(), Order: e.dec.Order(), DecimatedRate: e.front.OutputRate(),
		DecimatedBy: e.D, ShiftHz: e.shift}
}

// --- controls ---

// Resync drops the word framing and searches for the carrier again.
func (e *Engine) Resync() {
	e.status("resync requested")
	e.handleSitor(e.dec.Resync())
	e.rx.Reacquire()
}

// SetAutotune turns automatic carrier search/tracking on or off (off freezes the current carrier).
func (e *Engine) SetAutotune(on bool) {
	t := e.cfg.Tuning()
	t.Autotune = on
	e.cfg.SetTuning(t)
	e.rx.SetAutotune(on)
	e.status(fmt.Sprintf("autotune %v", map[bool]string{true: "on", false: "off"}[on]))
}

// SetCarrierAudioHz sets the carrier by hand (real audio input: audio frequency of the carrier centre).
func (e *Engine) SetCarrierAudioHz(hz float64) {
	t := e.cfg.Tuning()
	t.ManualCarrierHz = hz
	e.cfg.SetTuning(t)
	e.rx.SetCarrier(hz - e.shift)
	e.status(fmt.Sprintf("carrier set to %.1f Hz", hz))
}

// SetPolarity forces or releases the mark/space polarity.
func (e *Engine) SetPolarity(p sitor.Polarity) {
	e.cfg.Decoder.Polarity = p
	e.handleSitor(e.dec.SetPolarity(p))
	e.status("polarity " + p.String())
}

// SetUnshiftOnSpace switches the unshift-on-space option.
func (e *Engine) SetUnshiftOnSpace(on bool) {
	e.cfg.Decoder.UnshiftOnSpace = on
	e.dec.SetUnshiftOnSpace(on)
	e.status(fmt.Sprintf("unshift on space: %v", map[bool]string{true: "ON", false: "OFF"}[on]))
}

// Reconfigure applies a changed configuration to the running engine without restarting it: whatever
// config.RestartNeeded leaves out (see there) takes effect at once and nothing is lost but, for a few of the
// options, a moment of lock. Options that do need a restart are ignored here.
func (e *Engine) Reconfigure(cfg config.Config) {
	old := e.cfg
	// decoder: polarity and unshift-on-space report themselves; the rest is applied together
	pol, uos := cfg.Decoder.Polarity, cfg.Decoder.UnshiftOnSpace
	dc := cfg.Decoder
	dc.Polarity, dc.UnshiftOnSpace = old.Decoder.Polarity, old.Decoder.UnshiftOnSpace
	dc.Framing = old.Decoder.Framing
	if dc.ShiftHz = old.Decoder.ShiftHz; dc != old.Decoder {
		e.handleSitor(e.dec.SetConfig(dc))
	}
	if pol != old.Decoder.Polarity {
		e.SetPolarity(pol)
	}
	if uos != old.Decoder.UnshiftOnSpace {
		e.SetUnshiftOnSpace(uos)
	}
	e.cfg.Decoder = cfg.Decoder
	e.cfg.Decoder.ShiftHz = old.Decoder.ShiftHz
	e.seg.SetConfig(cfg.Segment)
	e.rx.SetConfig(cfg.Receiver)
	// carrier
	if cfg.Autotune() != old.Autotune() {
		e.SetAutotune(cfg.Autotune())
		if !cfg.Autotune() {
			e.SetCarrierAudioHz(cfg.ManualCarrierHz())
		}
	} else if !cfg.Autotune() && cfg.ManualCarrierHz() != old.ManualCarrierHz() {
		e.SetCarrierAudioHz(cfg.ManualCarrierHz())
	}
	e.cfg.SetTuning(cfg.Tuning())
	// meters
	m, om := cfg.Meters, old.Meters
	e.cfg.Meters = m
	if cfg.Channel.ViewOffset != old.Channel.ViewOffset {
		e.cfg.Channel.ViewOffset = cfg.Channel.ViewOffset
		e.spec = newSpec(e.fs, e.kind, e.ViewHz(), m)
	}
	e.qNeed = int(m.QualityWindowS * e.front.OutputRate())
	if m.SpectrumMaxHz != om.SpectrumMaxHz || m.SpectrumFFT != om.SpectrumFFT || m.RateHz != om.RateHz {
		e.spec = newSpec(e.fs, e.kind, e.ViewHz(), m)
		e.chs = newChanSpec(e.front.OutputRate(), e.spec.step, e.spec.n, m.SpectrumSmooth)
	} else {
		e.spec.alpha = m.SpectrumSmooth
		e.chs.alpha = m.SpectrumSmooth
	}
}

// --- internals ---

func (e *Engine) status(s string) {
	if e.h.Status != nil {
		e.h.Status(s)
	}
}

func (e *Engine) handleSyms(syms []fsk.Symbol) {
	e.scope.Symbols(syms)
	for _, s := range syms {
		if s.Locked {
			e.lockedSyms++
			e.eye[e.eyePos] = s.Soft
			if e.eyePos++; e.eyePos == eyeN {
				e.eyePos = 0
			}
			e.eyeFill = min(e.eyeFill+1, eyeN)
			e.eyeLastT = e.Now()
		}
	}
	e.handleSitor(e.dec.Process(syms))
}

func (e *Engine) handleSitor(evs []sitor.Event) {
	for _, ev := range evs {
		if ev.Kind == sitor.EvText {
			ts := float64(ev.Sym) / e.baud
			e.lastText = e.Now()
			if e.h.Text != nil {
				e.h.Text(ev.Text, ts)
			}
			e.seg.Feed(ev.Text, ts)
			continue
		}
		e.status(ev.String())
	}
}

// trackLogHz is how far the tracked carrier must move from the last logged offset before a tracking update is logged.
const trackLogHz = 10.0

func (e *Engine) reportCarrier() {
	for ; e.lastChanges < len(e.rx.Changes); e.lastChanges++ {
		c := e.rx.Changes[e.lastChanges]
		kind := "tracking"
		switch {
		case c.Manual:
			kind = "carrier set"
		case c.Verify:
			kind = "carrier re-acquired"
		case c.Search:
			kind = "carrier found"
		}
		// small tracking updates are routine: log them only when the carrier has moved noticeably
		if kind == "tracking" && math.Abs(c.F0-e.loggedF0) < trackLogHz {
			continue
		}
		e.loggedF0 = c.F0
		e.status(fmt.Sprintf("%s: offset %+.1f Hz", kind, c.F0))
	}
}

func (e *Engine) measureLevel(block []complex64) {
	for _, x := range block {
		a := float64(real(x))
		p := a * a
		if e.kind == iq.IQ {
			b := float64(imag(x))
			p += b * b
			a = math.Sqrt(p)
		} else if a < 0 {
			a = -a
		}
		if a > e.lvPeak {
			e.lvPeak = a
		}
		e.lvSum += p
	}
	e.lvN += len(block)
	if e.lvPeak >= 0.98 {
		e.lvClip = true
	}
}

// measureQuality turns the narrow-filter output statistics into the signal-quality figure. For a constant-envelope
// signal the peak-to-peak / standard-deviation ratio (crest factor) is 2*sqrt(2) = 2.83; for noise it is about 5-7.
// Quality maps the crest factor linearly from the noise value (0) to the clean value (100).
func (e *Engine) measureQuality() {
	max2, sum2, n := e.rx.Demod().TakeEnvelope()
	if n == 0 {
		return
	}
	if max2 > e.qMax2 {
		e.qMax2 = max2
	}
	e.qSum2 += sum2
	e.qN += n
	if e.qN < e.qNeed {
		return
	}
	q := 0.0
	if mean2 := e.qSum2 / float64(e.qN); mean2 > 1e-20 {
		cf := 2 * math.Sqrt2 * math.Sqrt(e.qMax2/mean2)
		m := e.cfg.Meters
		q = 100 * (m.QualityNoiseCF - cf) / (m.QualityNoiseCF - m.QualityGoodCF)
		q = math.Max(0, math.Min(100, q))
	}
	e.qCrest, e.qEye = q, -1
	if qe, ok := e.eyeQuality(); ok {
		e.qEye = qe
		w := math.Max(0, math.Min(1, e.cfg.Meters.QualityEyeWeight))
		q = w*qe + (1-w)*q
	}
	t := e.Now()
	e.qHist = append(e.qHist, qpoint{t, q})
	if cut := t - 900; len(e.qHist) > 64 && e.qHist[0].t < cut {
		i := 0
		for i < len(e.qHist) && e.qHist[i].t < cut {
			i++
		}
		e.qHist = append(e.qHist[:0], e.qHist[i:]...)
	}
	e.qLast = q
	e.qMax2, e.qSum2, e.qN = 0, 0, 0
}

// eyeN is the number of locked symbols (2 s) behind one eye measurement; eyeMin of them are needed.
const (
	eyeN   = 200
	eyeMin = 100
)

// eyeQuality rates the decisions themselves. The soft value of a locked symbol is about +-A (A = half the shift) plus
// noise. With M2 = mean(x^2) and M4 = mean(x^4) of the last symbols, the signal power of a two-level signal in
// Gaussian noise is S = sqrt((3*M2^2 - M4)/2) and the noise power M2 - S (the M2M4 estimator, independent of any
// filter or reference band). The ratio S/N is mapped linearly in dB from QualityEyeLowDB (0) to QualityEyeHighDB
// (100). It is not available when the symbols are not locked or too few have arrived lately.
func (e *Engine) eyeQuality() (float64, bool) {
	if e.eyeFill < eyeMin || e.Now()-e.eyeLastT > 3 {
		return 0, false
	}
	var m2, m4 float64
	for _, x := range e.eye[:e.eyeFill] {
		x2 := x * x
		m2 += x2
		m4 += x2 * x2
	}
	n := float64(e.eyeFill)
	m2, m4 = m2/n, m4/n
	if m2 < 1e-12 {
		return 0, true
	}
	s2 := (3*m2*m2 - m4) / 2
	if s2 <= 0 {
		return 0, true
	}
	sig := math.Sqrt(s2)
	noise := m2 - sig
	m := e.cfg.Meters
	var db float64
	if noise <= 1e-9*sig {
		db = m.QualityEyeHighDB
	} else {
		db = 10 * math.Log10(sig/noise)
	}
	return math.Max(0, math.Min(100, 100*(db-m.QualityEyeLowDB)/(m.QualityEyeHighDB-m.QualityEyeLowDB))), true
}

// Quality returns the mean signal quality (0..100) of the analysis windows overlapping stream times a..b, or 0.
func (e *Engine) Quality(a, b float64) float64 {
	w := e.cfg.Meters.QualityWindowS
	var sum float64
	n := 0
	for i := len(e.qHist) - 1; i >= 0; i-- {
		p := e.qHist[i]
		if p.t-w > b {
			continue
		}
		if p.t < a {
			break
		}
		sum += p.q
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func (e *Engine) emitMeters() {
	if e.h.Meters == nil {
		e.lvPeak, e.lvSum, e.lvN, e.lvClip = 0, 0, 0, false
		e.spec.take()
		return
	}
	lv := Level{Peak: e.lvPeak, Clip: e.lvClip}
	if e.lvN > 0 {
		lv.RMS = math.Sqrt(e.lvSum / float64(e.lvN))
	}
	e.lvPeak, e.lvSum, e.lvN, e.lvClip = 0, 0, 0, false
	sync := SyncLost
	switch {
	case e.dec.Locked():
		sync = SyncFull
	case e.rx.Demod().TimingLocked():
		sync = SyncPre
	}
	f0 := e.rx.F0()
	m := Meters{StreamS: e.Now(), Level: lv, Quality: e.qLast, Sync: sync, Data: e.Now()-e.lastText < 2,
		Carrier: Carrier{Have: e.rx.HaveCarrier(), Autotune: e.rx.Autotune(), OffsetHz: f0, AudioHz: e.shift + f0}}
	if sp := e.spec.take(); sp != nil {
		sp.CarrierHz = e.shift + f0
		sp.FullScaleDB = e.spec.fullScaleDB
		if e.cfg.Meters.SpectrumChannel {
			ch := make([]float32, len(sp.Filtered))
			if e.chs.frame(ch, sp.StartHz, sp.StepHz, e.shift+f0) {
				sp.Channel = ch
			}
		}
		m.Spectrum = sp
	}
	e.h.Meters(m)
}
