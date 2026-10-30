package fsk

import (
	"math"
	"math/cmplx"

	"navtex/internal/ddc"
)

// Params are the demodulator settings; DefaultParams gives the reference values.
type Params struct {
	Fs           float64 `json:"-"` // complex sample rate after the DDC (~4 kHz); set by the pipeline
	Baud         float64 `json:"baud" unit:"baud" doc:"Symbol rate of the NAVTEX FSK signal"`
	ShiftHz      float64 `json:"shift_hz" unit:"Hz" doc:"Mark/space frequency spacing (tones at carrier +/- half of this)"`
	NarrowCutoff float64 `json:"channel_cutoff_hz" unit:"Hz" doc:"Low-pass cutoff of the narrow channel filter after the carrier is moved to 0 Hz"`
	PresentDB    float64 `json:"presence_db" unit:"dB" doc:"Signal counts as present when the tone band exceeds the neighbouring bands by this much"`
	PresentOff   float64 `json:"presence_offset_hz" unit:"Hz" doc:"Distance of the two comparison bands from the carrier"`
	BlkSyms      int     `json:"timing_block_symbols" doc:"Symbols per timing-estimation block (100 = 1 s)"`
	Alpha        float64 `json:"timing_gain" doc:"Gain of the first-order symbol-timing tracker (0..1)"`
	ConfThr      float64 `json:"timing_confidence" doc:"Timing confidence needed to lock and stay locked"`
	MaxLost      int     `json:"timing_lost_blocks" doc:"Consecutive bad blocks before timing lock is dropped"`
}

func DefaultParams(fs float64) Params {
	return Params{Fs: fs, Baud: 100, ShiftHz: 170, NarrowCutoff: 170, PresentDB: 1.5, PresentOff: 350,
		BlkSyms: 100, Alpha: 0.4, ConfThr: 0.02, MaxLost: 3}
}

// Symbol is one integrate-and-dump result. Soft is in Hz (+ = higher tone), 0 when not Locked.
type Symbol struct {
	Soft   float64
	Locked bool
	Center float64 // symbol centre as a sample index of the demodulator's filtered stream (for the scope)
}

// arm is a mixer followed by the narrow low-pass.
type arm struct {
	mix *ddc.Mixer
	fir *ddc.Decimator
	buf []complex128
}

func newArm(fs, shift float64, taps []float64) *arm {
	return &arm{mix: ddc.NewMixer(fs, shift), fir: ddc.NewDecimator(1, taps)}
}

func (a *arm) process(in []complex128) []complex128 {
	if cap(a.buf) < len(in) {
		a.buf = make([]complex128, len(in))
	}
	a.buf = a.buf[:len(in)]
	a.mix.Process128(in, a.buf)
	return a.fir.Process(a.buf)
}

type pending struct {
	c      float64
	locked bool
}

// Demod turns the blanked complex baseband (~4 kHz) into soft symbols. It runs the narrow filter at carrier offset
// F0, discriminator, presence detector, timing tracker and integrate-and-dump. Symbols of a 100-symbol block are
// released once the block has been analysed and the samples of their windows have arrived (about 1-2 s latency).
type Demod struct {
	p                Params
	sps, blkLen      float64
	main, hi, lo     *arm
	f0               float64
	n                int64 // samples consumed
	prev             complex128
	zbuf             []complex128 // z history starting at absolute index zBase
	zBase            int64
	block            int64 // current timing block
	edgeEnd          int64 // sample index at which the current block ends
	zc               complex128
	su, pin, pref    float64
	trkLocked        bool
	trkD             float64
	trkLost          int
	pend             []pending
	nextSym          int64
	out              []Symbol
	Conf             []float64 // per-block timing confidence (for diagnostics)
	Present          []bool
	BlockLocked      []bool
	envMax2, envSum2 float64 // narrow-filter output statistics since the last TakeEnvelope
	envN             int
	KeepNarrowOutput bool // diagnostics: append the narrow-filter output to Narrow
	Narrow           []complex128
	delay            int // group delay of the narrow filter in samples
	// OnBlock, if set, is called for every block with the sample index of its first output, the (blanked) input
	// samples and the narrow-filter output (same length). The slices are only valid during the call.
	OnBlock func(n0 int64, x, y []complex128)
}

// NewDemod creates a demodulator with carrier offset f0 (Hz from the DDC centre).
func NewDemod(p Params, f0 float64) *Demod {
	taps := ddc.DesignLowpass(p.Fs, p.NarrowCutoff, 100, 60)
	d := &Demod{p: p, sps: p.Fs / p.Baud, f0: f0}
	d.delay = (len(taps) - 1) / 2
	d.blkLen = float64(p.BlkSyms) * d.sps
	d.main = newArm(p.Fs, f0, taps)
	d.hi = newArm(p.Fs, f0+p.PresentOff, taps)
	d.lo = newArm(p.Fs, f0-p.PresentOff, taps)
	d.edgeEnd = int64(math.RoundToEven(d.blkLen))
	return d
}

// SetLive changes the settings that need no redesign of the filters or timing blocks: presence threshold and
// comparison-band distance, timing gain, confidence and lost-block count.
func (d *Demod) SetLive(p Params) {
	d.p.PresentDB, d.p.Alpha, d.p.ConfThr, d.p.MaxLost = p.PresentDB, p.Alpha, p.ConfThr, p.MaxLost
	if p.PresentOff != d.p.PresentOff {
		d.p.PresentOff = p.PresentOff
		d.SetF0(d.f0)
	}
}

// FilterDelay is the group delay of the narrow filter in samples: the output at index n shows the input at n-delay.
func (d *Demod) FilterDelay() int { return d.delay }

// F0 returns the current carrier offset.
func (d *Demod) F0() float64 { return d.f0 }

// TimingLocked reports whether the symbol-timing tracker is currently locked.
func (d *Demod) TimingLocked() bool { return d.trkLocked }

// TakeEnvelope returns the maximum squared magnitude, the sum of squared magnitudes and the sample count of the
// narrow-filter output since the previous call, and resets them. It feeds the signal-quality (crest factor) figure.
func (d *Demod) TakeEnvelope() (max2, sum2 float64, n int) {
	max2, sum2, n = d.envMax2, d.envSum2, d.envN
	d.envMax2, d.envSum2, d.envN = 0, 0, 0
	return
}

// SetF0 retunes the carrier offset; phases stay continuous.
func (d *Demod) SetF0(f0 float64) {
	d.f0 = f0
	d.main.mix.SetShift(d.p.Fs, f0)
	d.hi.mix.SetShift(d.p.Fs, f0+d.p.PresentOff)
	d.lo.mix.SetShift(d.p.Fs, f0-d.p.PresentOff)
}

func clip(x, lim float64) float64 { return math.Max(-lim, math.Min(lim, x)) }

// Process consumes blanked baseband samples and returns any symbols that became ready.
// The returned slice is valid until the next call.
func (d *Demod) Process(in []complex128) []Symbol {
	d.out = d.out[:0]
	y := d.main.process(in)
	a := d.hi.process(in)
	b := d.lo.process(in)
	if d.KeepNarrowOutput {
		d.Narrow = append(d.Narrow, y...)
	}
	if d.OnBlock != nil && len(y) == len(in) {
		d.OnBlock(d.n, in, y)
	}
	fs := d.p.Fs
	for i, yi := range y {
		a2 := real(yi)*real(yi) + imag(yi)*imag(yi)
		d.envSum2 += a2
		d.envN++
		if a2 > d.envMax2 {
			d.envMax2 = a2
		}
		z := yi * cmplx.Conj(d.prev)
		d.prev = yi
		d.zbuf = append(d.zbuf, z)
		d.pin += real(yi)*real(yi) + imag(yi)*imag(yi)
		d.pref += 0.5 * (real(a[i])*real(a[i]) + imag(a[i])*imag(a[i]) + real(b[i])*real(b[i]) + imag(b[i])*imag(b[i]))
		f := cmplx.Phase(z) * fs / (2 * math.Pi)
		fn := clip(f/(d.p.ShiftHz/2), 1.5)
		u := cmplx.Abs(z) * fn * fn
		ph := -2 * math.Pi * math.Mod(float64(d.n), d.sps) / d.sps
		d.zc += complex(u*math.Cos(ph), u*math.Sin(ph))
		d.su += u
		d.n++
		if d.n == d.edgeEnd {
			d.endBlock()
		}
	}
	d.emit(false)
	d.trim()
	return d.out
}

func (d *Demod) endBlock() {
	p := d.p
	conf := cmplx.Abs(d.zc) / (d.su + 1e-12)
	x := -cmplx.Phase(d.zc) / (2 * math.Pi) * d.sps
	tau := x - d.sps*math.Floor(x/d.sps)
	present := 10*math.Log10((d.pin+1e-20)/(d.pref+1e-20)) >= p.PresentDB
	if conf >= p.ConfThr && present {
		if !d.trkLocked {
			d.trkD, d.trkLocked = tau, true
		} else {
			e := tau - d.trkD + d.sps/2
			e = e - d.sps*math.Floor(e/d.sps) - d.sps/2
			d.trkD += p.Alpha * e
		}
		d.trkLost = 0
	} else {
		d.trkLost++
		if d.trkLost >= p.MaxLost {
			d.trkLocked = false
		}
	}
	d.Conf = append(d.Conf, conf)
	d.Present = append(d.Present, present)
	d.BlockLocked = append(d.BlockLocked, d.trkLocked)
	for m := d.block * int64(p.BlkSyms); m < (d.block+1)*int64(p.BlkSyms); m++ {
		d.pend = append(d.pend, pending{c: float64(m)*d.sps + d.trkD, locked: d.trkLocked})
	}
	d.block++
	d.edgeEnd = int64(math.RoundToEven(float64(d.block+1) * d.blkLen))
	d.zc, d.su, d.pin, d.pref = 0, 0, 0, 0
}

// windowSum returns the sum of z over [a, b] with linear interpolation of the cumulative sum at the ends.
func (d *Demod) windowSum(a, b float64) complex128 {
	ka, kb := int64(math.Floor(a)), int64(math.Floor(b))
	var s complex128
	for k := ka; k < kb; k++ {
		s += d.zbuf[k-d.zBase]
	}
	if fb := b - float64(kb); fb > 0 {
		s += complex(fb, 0) * d.zbuf[kb-d.zBase]
	}
	s -= complex(a-float64(ka), 0) * d.zbuf[ka-d.zBase]
	return s
}

func (d *Demod) emit(flush bool) {
	lim := d.p.ShiftHz / 2 * 1.5
	for len(d.pend) > 0 {
		p := d.pend[0]
		a, b := p.c-d.sps/2, p.c+d.sps/2
		switch {
		case a < 0:
			d.out = append(d.out, Symbol{Center: p.c})
		case b <= float64(d.n) && int64(math.Floor(a)) < d.zBase:
			d.out = append(d.out, Symbol{Center: p.c}) // history already discarded (should not happen): report unlocked
		case b <= float64(d.n):
			soft := clip(cmplx.Phase(d.windowSum(a, b))*d.p.Fs/(2*math.Pi), lim)
			if !p.locked {
				soft = 0
			}
			d.out = append(d.out, Symbol{Soft: soft, Locked: p.locked, Center: p.c})
		case flush:
			d.out = append(d.out, Symbol{Center: p.c})
		default:
			return
		}
		d.pend = d.pend[1:]
		d.nextSym++
	}
}

// Flush releases symbols still waiting for samples that will never arrive (reported as not locked, soft 0).
func (d *Demod) Flush() []Symbol {
	d.out = d.out[:0]
	d.emit(true)
	return d.out
}

// trim drops z history that no pending or future symbol window can need.
func (d *Demod) trim() {
	// The next block's symbol windows start near block*blkLen + trkD, and trkD drifts without bound when the
	// transmitter's symbol clock differs from ours, so the history to keep follows it (the tracker moves it by at
	// most a fraction of a symbol per block).
	keep := int64(math.Floor(float64(d.block)*d.blkLen + math.Min(d.trkD, 0) - 3*d.sps))
	if k := int64(math.Floor(float64(d.block)*d.blkLen - 2*d.sps)); k < keep {
		keep = k
	}
	if len(d.pend) > 0 {
		if k := int64(math.Floor(d.pend[0].c-d.sps/2)) - 1; k < keep {
			keep = k
		}
	}
	if keep > d.zBase {
		if int(keep-d.zBase) > len(d.zbuf) {
			keep = d.zBase + int64(len(d.zbuf))
		}
		n := int(keep - d.zBase)
		d.zbuf = append(d.zbuf[:0], d.zbuf[n:]...)
		d.zBase = keep
	}
}
