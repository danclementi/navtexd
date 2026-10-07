package engine

import (
	"math"
	"math/cmplx"
	"sync"

	"navtex/internal/fsk"
)

// Scope keeps the last seconds of the intermediate signals of the demodulator so a browser can display them like an
// oscilloscope. All signals are stored on the sample axis of the demodulator's filtered stream, shifted for their own
// processing delay, so that the decided bits line up under the signal that produced them.
//
// Lanes (all at the demodulator sample rate, about 4 kHz):
//
//	audio  real part of the baseband input (shifted by the channel filter delay)
//	filt   real part of the channel-filter output
//	env    magnitude of the channel-filter output
//	disc   instantaneous frequency (discriminator), Hz from the carrier
//	hi, lo magnitudes of one-symbol matched filters at the high and low tone
//	soft   integrate-and-dump value of each symbol (Hz), held over the symbol
//	bit    +1 / -1 for the decided bit (high tone = +1), 0 when timing is not locked
type Scope struct {
	mu    sync.Mutex
	fs    float64
	sps   float64
	delay int
	size  int
	n     int64 // number of samples written (index of the next sample)

	audio, filt, env, disc, hi, lo []float32
	syms                           []scopeSym

	shift   float64 // half the tone spacing, Hz
	winLen  int
	prod    [2][]complex128 // products of y with the tone rotator over the last winLen samples
	sum     [2]complex128
	prev    complex128
	started bool
}

type scopeSym struct {
	c    float64
	soft float32
	bit  int8
}

const scopeSeconds = 24

// NewScope makes a scope for a demodulator running at fs with the given symbol rate, tone spacing and filter delay.
func NewScope(fs, baud, shiftHz float64, delay int) *Scope {
	s := &Scope{fs: fs, sps: fs / baud, delay: delay, shift: shiftHz / 2}
	s.size = int(scopeSeconds * fs)
	for _, p := range []*[]float32{&s.audio, &s.filt, &s.env, &s.disc, &s.hi, &s.lo} {
		*p = make([]float32, s.size)
	}
	s.winLen = int(math.Round(s.sps))
	for i := range s.prod {
		s.prod[i] = make([]complex128, s.winLen)
	}
	return s
}

func (s *Scope) at(a []float32, i int64) *float32 {
	return &a[int(((i%int64(s.size))+int64(s.size))%int64(s.size))]
}

// Block records one block (see fsk.Demod.OnBlock).
func (s *Scope) Block(n0 int64, x, y []complex128) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range y {
		n := n0 + int64(i)
		*s.at(s.audio, n+int64(s.delay)) = float32(real(x[i]))
		*s.at(s.filt, n) = float32(real(y[i]))
		*s.at(s.env, n) = float32(cmplx.Abs(y[i]))
		f := 0.0
		if s.started {
			f = cmplx.Phase(y[i]*cmplx.Conj(s.prev)) * s.fs / (2 * math.Pi)
		}
		s.prev, s.started = y[i], true
		*s.at(s.disc, n) = float32(f)
		// matched filters: sliding one-symbol sums of y * exp(-j 2 pi f t) at +-shift
		slot := int(n % int64(s.winLen))
		for k, sign := range [2]float64{1, -1} {
			sn, cs := math.Sincos(-2 * math.Pi * sign * s.shift * float64(n) / s.fs)
			p := y[i] * complex(cs, sn)
			s.sum[k] += p - s.prod[k][slot]
			s.prod[k][slot] = p
		}
		mid := n - int64(s.winLen/2)
		*s.at(s.hi, mid) = float32(cmplx.Abs(s.sum[0]) / float64(s.winLen))
		*s.at(s.lo, mid) = float32(cmplx.Abs(s.sum[1]) / float64(s.winLen))
	}
	s.n = n0 + int64(len(y))
}

// Symbols records decided symbols (see fsk.Symbol).
func (s *Scope) Symbols(syms []fsk.Symbol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, y := range syms {
		b := int8(0)
		if y.Locked {
			b = -1
			if y.Soft > 0 {
				b = 1
			}
		}
		s.syms = append(s.syms, scopeSym{c: y.Center, soft: float32(y.Soft), bit: b})
	}
	if cut := float64(s.n) - float64(s.size)*0.9; len(s.syms) > 0 && s.syms[0].c < cut {
		i := 0
		for i < len(s.syms) && s.syms[i].c < cut {
			i++
		}
		s.syms = append(s.syms[:0], s.syms[i:]...)
	}
}

// Trigger kinds for Sweep.
const (
	TrigNone = iota
	TrigRise // bit 0 -> 1 (low tone to high tone)
	TrigFall
)

// Sweep is one captured screen.
type Sweep struct {
	Head    float64              `json:"head"` // seconds up to which bits are decided
	T0      float64              `json:"t0"`   // start of the sweep, seconds
	Span    float64              `json:"span"`
	Fs      float64              `json:"fs"` // points per second of the traces
	Pre     float64              `json:"pre"`
	Traces  map[string][]float32 `json:"traces"`
	Bit     []int8               `json:"bit"`
	Clock   []float64            `json:"clock"` // symbol centres, seconds from the start of the sweep
	Trigger float64              `json:"trigger"`
}

// Head returns the time in seconds up to which symbols have been decided (0 when none).
func (s *Scope) Head() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.head() / s.fs
}

func (s *Scope) head() float64 {
	if len(s.syms) == 0 {
		return 0
	}
	return s.syms[len(s.syms)-1].c + s.sps/2
}

// Sweep captures span seconds. after is the end time of the previous sweep (0 = the newest sweep available);
// pre is the fraction of the span shown before the trigger. It returns nil when no complete sweep is available yet.
func (s *Scope) Sweep(span float64, trig int, pre, after float64, maxPts int) *Sweep {
	s.mu.Lock()
	defer s.mu.Unlock()
	head := s.head()
	if head == 0 {
		return nil
	}
	win := span * s.fs
	oldest := math.Max(0, float64(s.n)-float64(s.size)*0.9)
	var t0 float64
	trigAt := -1.0
	afterI := after * s.fs
	find := func(from float64, latest bool) (float64, bool) {
		var res float64
		ok := false
		for i := 1; i < len(s.syms); i++ {
			a, b := s.syms[i-1], s.syms[i]
			if a.bit == 0 || b.bit == 0 || a.bit == b.bit || b.c-a.c > 1.5*s.sps {
				continue
			}
			if (trig == TrigRise) != (b.bit > 0) {
				continue
			}
			edge := (a.c + b.c) / 2
			st := edge - pre*win
			if st < from || st < oldest {
				continue
			}
			if st+win > head {
				break
			}
			res, ok = edge, true
			if !latest {
				return res, true
			}
		}
		return res, ok
	}
	switch {
	case trig == TrigNone && after <= 0:
		t0 = head - win
	case trig == TrigNone:
		t0 = math.Max(afterI, oldest)
		if t0+win > head {
			return nil
		}
	default:
		edge, ok := find(math.Max(afterI, oldest), after <= 0)
		if !ok {
			return nil
		}
		trigAt = edge
		t0 = edge - pre*win
	}
	if t0 < oldest {
		t0 = oldest
	}
	if t0 < 0 {
		t0 = 0
	}
	step := 1
	if maxPts > 0 && int(win) > maxPts {
		step = int(math.Ceil(win / float64(maxPts)))
	}
	n := int(win) / step
	sw := &Sweep{Head: head / s.fs, T0: t0 / s.fs, Span: span, Fs: s.fs / float64(step), Pre: pre, Traces: map[string][]float32{}}
	i0 := int64(math.Round(t0))
	lane := func(a []float32) []float32 {
		out := make([]float32, n)
		for k := range out {
			out[k] = *s.at(a, i0+int64(k*step))
		}
		return out
	}
	sw.Traces["audio"], sw.Traces["filt"], sw.Traces["env"], sw.Traces["disc"] = lane(s.audio), lane(s.filt), lane(s.env), lane(s.disc)
	sw.Traces["hi"], sw.Traces["lo"] = lane(s.hi), lane(s.lo)
	soft := make([]float32, n)
	sw.Bit = make([]int8, n)
	for _, y := range s.syms {
		a, b := y.c-s.sps/2, y.c+s.sps/2
		if b < float64(i0) || a > float64(i0)+win {
			continue
		}
		if t := (y.c - float64(i0)) / s.fs; t >= 0 && t <= span {
			sw.Clock = append(sw.Clock, math.Round(t*1e4)/1e4)
		}
		ka, kb := int(math.Ceil((a-float64(i0))/float64(step))), int(math.Ceil((b-float64(i0))/float64(step)))
		for k := max(ka, 0); k < kb && k < n; k++ {
			soft[k], sw.Bit[k] = y.soft, y.bit
		}
	}
	sw.Traces["soft"] = soft
	if trigAt >= 0 {
		sw.Trigger = (trigAt - float64(i0)) / s.fs
	} else {
		sw.Trigger = -1
	}
	// quantise to 12 bits of the trace's own range to keep the JSON small
	for k, a := range sw.Traces {
		m := 0.0
		for _, v := range a {
			m = math.Max(m, math.Abs(float64(v)))
		}
		if m > 0 {
			q := m / 4096
			for i, v := range a {
				a[i] = float32(math.Round(float64(v)/q) * q)
			}
		}
		sw.Traces[k] = a
	}
	return sw
}
