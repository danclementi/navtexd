package fsk

import (
	"math"
	"sort"
)

// ReceiverConfig holds the settings of the streaming front end; DefaultReceiverConfig gives sensible values.
type ReceiverConfig struct {
	Params        `json:"demod"`
	BlankK        float64 `json:"blanker_threshold" unit:"x median" doc:"Impulse blanker: zero samples larger than this multiple of the local median magnitude (0 = off)"`
	SearchHz      float64 `json:"search_range_hz" unit:"Hz" doc:"Carrier search range around the channel frequency"`
	SearchWinS    float64 `json:"search_window_s" unit:"s" doc:"Length of signal analysed by each carrier search"`
	SearchHopS    float64 `json:"search_interval_s" unit:"s" doc:"How often to search while no carrier is known"`
	VerifyS       float64 `json:"search_verify_s" unit:"s" doc:"While locked, repeat the carrier search this often and retune if a clearly better position is found (0 = off)"`
	VerifyVetoHz  float64 `json:"search_verify_veto_hz" unit:"Hz" doc:"Do not retune on the repeated search while the carrier tracker reports a healthy signal: the mark and space symbols well separated and centred within this many Hz of the current position (0 = never veto)"`
	MinContrast   float64 `json:"search_min_contrast" doc:"Comb-peak to median ratio needed to accept a carrier (noise ~1.2, signals >= 3)"`
	LoseAfterS    float64 `json:"lose_carrier_after_s" unit:"s" doc:"Seconds without timing lock before the carrier is forgotten and searched again"`
	TrackWindow   int     `json:"track_window_symbols" doc:"Locked symbols used by the carrier-frequency tracker"`
	TrackMinEach  int     `json:"track_min_each_sign" doc:"Minimum mark and space symbols needed for a tracking update"`
	TrackMinTotal int     `json:"track_min_symbols" doc:"Minimum symbols in the window needed for a tracking update"`
	TrackInterval int     `json:"track_interval_symbols" doc:"New locked symbols between tracking updates"`
	TrackGain     float64 `json:"track_gain" doc:"Fraction of the measured frequency error corrected per update"`
	TrackMaxStep  float64 `json:"track_max_step_hz" unit:"Hz" doc:"Largest single frequency correction"`
	TrackMaxExcur float64 `json:"track_max_excursion_hz" unit:"Hz" doc:"Furthest the tracker may move the carrier from where the search placed it (0 = unlimited)"`
}

func DefaultReceiverConfig(fs float64) ReceiverConfig {
	return ReceiverConfig{Params: DefaultParams(fs), BlankK: 6.0, SearchHz: 400, SearchWinS: 4, SearchHopS: 1,
		VerifyS: 8, MinContrast: 2.5, LoseAfterS: 10, TrackWindow: 300, TrackMinEach: 60, TrackMinTotal: 200, TrackInterval: 100,
		TrackGain: 0.7, TrackMaxStep: 20, TrackMaxExcur: 30, VerifyVetoHz: 30}
}

// F0Change records a carrier offset update, for diagnostics.
type F0Change struct {
	Sample int64 // input sample index (at the DDC output rate) when it was applied
	F0     float64
	Search bool // true for an acquisition from the comb search, false for a tracking update
	Verify bool // an acquisition made by the periodic re-check while locked
	Manual bool // set by hand (SetCarrier)
}

// Receiver is the streaming front end: blanker, carrier acquisition, demodulator and carrier tracking.
// Feed it the DDC output; it returns soft symbols.
type Receiver struct {
	cfg     ReceiverConfig
	blanker *Blanker
	demod   *Demod
	search  *CarrierSearch

	win        []complex128 // recent blanked samples for the carrier search
	haveCarr   bool
	nIn        int64 // blanked samples passed to the demod
	symIdx     int64
	ignoreBel  int64 // symbols with index below this were made at an older f0
	ring       []float64
	newLocked  int
	unlockedBl int
	nextVerify int64
	manual     bool    // autotuning off: the carrier offset is fixed until SetCarrier
	f0Acq      float64 // offset where the last search placed the carrier; the tracker stays near it
	healthyAt  int64   // symbol index of the latest tracker update that found well separated mark and space symbols centred on f0

	Changes []F0Change
}

func NewReceiver(cfg ReceiverConfig) *Receiver {
	return &Receiver{
		cfg:     cfg,
		blanker: NewBlanker(cfg.Fs, cfg.BlankK),
		demod:   NewDemod(cfg.Params, 0),
		search:  NewCarrierSearch(cfg.Fs, cfg.ShiftHz, cfg.SearchHz),
	}
}

// SetConfig applies the options that can change while running: blanker threshold (unless it switches blanking on
// or off), carrier search and tracking settings, and the presence/timing thresholds of the demodulator. The sample
// rate, baud rate, tone spacing, channel filter and timing block length are kept (they need a new receiver).
func (r *Receiver) SetConfig(c ReceiverConfig) {
	old := r.cfg
	c.Params.Fs, c.Params.Baud, c.Params.ShiftHz = old.Fs, old.Baud, old.ShiftHz
	c.Params.NarrowCutoff, c.Params.BlkSyms = old.NarrowCutoff, old.BlkSyms
	if !r.blanker.SetK(c.BlankK) {
		c.BlankK = old.BlankK
	}
	r.cfg = c
	r.search.Search = c.SearchHz
	r.demod.SetLive(c.Params)
}

// F0 returns the current carrier offset estimate; HaveCarrier says whether it is backed by a detection.
func (r *Receiver) F0() float64       { return r.demod.F0() }
func (r *Receiver) HaveCarrier() bool { return r.haveCarr }
func (r *Receiver) Demod() *Demod     { return r.demod }
func (r *Receiver) Blanker() *Blanker { return r.blanker }

// SetAutotune turns automatic carrier search and tracking on or off. Off keeps the current offset (see SetCarrier).
func (r *Receiver) SetAutotune(on bool) {
	r.manual = !on
	if r.manual {
		r.haveCarr = true
		r.ring = r.ring[:0]
	} else {
		r.haveCarr = false // search again from scratch
		r.unlockedBl = 0
	}
}

// Autotune reports whether automatic carrier search and tracking is on.
func (r *Receiver) Autotune() bool { return !r.manual }

// SetCarrier moves the carrier offset (Hz from the DDC centre) by hand.
func (r *Receiver) SetCarrier(f0 float64) {
	r.setF0(f0, false)
	r.Changes[len(r.Changes)-1].Manual = true
}

// Reacquire forgets the current carrier and searches again (no effect while autotuning is off).
func (r *Receiver) Reacquire() {
	if !r.manual {
		r.haveCarr = false
		r.ring = r.ring[:0]
		r.unlockedBl = 0
	}
}

func (r *Receiver) setF0(f0 float64, search bool) {
	r.demod.SetF0(f0)
	if search {
		r.healthyAt = 0 // a new search result has not been checked against the symbols yet
		r.f0Acq = f0
	}
	r.Changes = append(r.Changes, F0Change{Sample: r.nIn, F0: f0, Search: search})
	// symbols of the block in progress and earlier were produced with the old f0
	r.ignoreBel = (r.demod.block + 2) * int64(r.cfg.BlkSyms)
	r.ring = r.ring[:0]
	r.newLocked = 0
}

// Process consumes DDC output and returns soft symbols (valid until the next call).
func (r *Receiver) Process(in []complex128) []Symbol {
	return r.run(r.blanker.Process(in), false)
}

// Flush ends the stream and releases the remaining symbols.
func (r *Receiver) Flush() []Symbol {
	return r.run(r.blanker.Flush(), true)
}

func (r *Receiver) run(xb []complex128, flush bool) []Symbol {
	winLen := int(r.cfg.SearchWinS * r.cfg.Fs)
	hop := int(r.cfg.SearchHopS * r.cfg.Fs)
	var out []Symbol
	// Feed in pieces that end on multiples of one hop of blanked samples, so every decision is made at the same
	// sample index whatever the block size of the caller (bit-identical output for any partition).
	for len(xb) > 0 {
		n := min(len(xb), hop-int(r.nIn%int64(hop)))
		// Carrier search decisions are taken at the start of a hop, on the samples before it.
		if !r.manual && !r.haveCarr && r.nIn%int64(hop) == 0 && len(r.win) >= winLen {
			if f0, contrast := r.search.Estimate(r.win); contrast >= r.cfg.MinContrast {
				r.setF0(f0, true)
				r.haveCarr = true
				r.unlockedBl = 0
			}
		}
		if !r.manual && r.haveCarr && r.cfg.VerifyS > 0 && len(r.win) >= winLen && r.nIn%int64(hop) == 0 &&
			r.nIn >= r.nextVerify {
			r.nextVerify = r.nIn + int64(r.cfg.VerifyS*r.cfg.Fs)
			r.verify()
		}
		piece := xb[:n]
		xb = xb[n:]
		r.nIn += int64(n)
		r.win = append(r.win, piece...)
		if len(r.win) > winLen {
			r.win = append(r.win[:0], r.win[len(r.win)-winLen:]...)
		}
		nb := len(r.demod.BlockLocked)
		syms := r.demod.Process(piece)
		out = r.after(out, syms, nb)
		// Retune only on hop boundaries: by then the set of symbols released is independent of how the caller
		// chunked the input, which keeps the output identical for any block size.
		if !r.manual && r.haveCarr && r.nIn%int64(hop) == 0 && r.newLocked >= r.cfg.TrackInterval {
			r.track()
		}
	}
	if flush {
		out = r.after(out, r.demod.Flush(), len(r.demod.BlockLocked))
	}
	return out
}

// after post-processes demod output: forwards symbols, tracks the carrier, and detects carrier loss.
func (r *Receiver) after(out, syms []Symbol, nbBefore int) []Symbol {
	for _, s := range syms {
		idx := r.symIdx
		r.symIdx++
		out = append(out, s)
		if r.haveCarr && s.Locked && idx >= r.ignoreBel {
			r.ring = append(r.ring, s.Soft)
			if len(r.ring) > r.cfg.TrackWindow {
				r.ring = r.ring[1:]
			}
			r.newLocked++
		}
	}
	for _, l := range r.demod.BlockLocked[nbBefore:] {
		if l {
			r.unlockedBl = 0
		} else if r.unlockedBl++; !r.manual && r.haveCarr && float64(r.unlockedBl) >= r.cfg.LoseAfterS {
			r.haveCarr = false
			r.ring = r.ring[:0]
		}
	}
	return out
}

// track nudges f0 so the mark and space soft-symbol clusters are symmetric about zero.
func (r *Receiver) track() {
	if len(r.ring) < r.cfg.TrackMinTotal {
		return
	}
	var pos, neg []float64
	for _, v := range r.ring {
		if v > 0 {
			pos = append(pos, v)
		} else if v < 0 {
			neg = append(neg, v)
		}
	}
	if len(pos) < r.cfg.TrackMinEach || len(neg) < r.cfg.TrackMinEach {
		return
	}
	sort.Float64s(pos)
	sort.Float64s(neg)
	corr := 0.5 * (pos[len(pos)/2] + neg[len(neg)/2])
	r.newLocked = 0
	// the tones are well separated and the middle between them is near f0: the demodulator sits on the signal
	if half := 0.5 * r.cfg.ShiftHz; math.Min(pos[len(pos)/2], -neg[len(neg)/2]) >= 0.4*half && math.Abs(corr) <= r.cfg.VerifyVetoHz {
		r.healthyAt = r.symIdx
	}
	if corr > -0.5 && corr < 0.5 {
		return
	}
	step := clip(r.cfg.TrackGain*corr, r.cfg.TrackMaxStep)
	f0 := r.demod.F0() + step
	if m := r.cfg.TrackMaxExcur; m > 0 {
		// a tracker fed by noise can walk away until it sits on one tone; keep it near the search result
		f0 = r.f0Acq + clip(f0-r.f0Acq, m)
		if f0 == r.demod.F0() {
			return
		}
	}
	r.setF0(f0, false)
}

// verify repeats the comb search on the recent signal while locked. If the best position is far from the current
// one and clearly stronger, the carrier is retuned there (the tracker or the first search had gone wrong).
func (r *Receiver) verify() {
	cur := r.demod.F0()
	f0, contrast, curScore, best := r.search.EstimateRef(r.win, cur)
	if contrast < r.cfg.MinContrast || math.Abs(f0-cur) < 25 || best < 1.5*curScore {
		return
	}
	// The comb search is coarse (it can sit 20 Hz from the true carrier on a signal whose tones differ in strength),
	// the tracker measures the symbols themselves: while that finds the signal healthy, leave the carrier alone.
	if r.cfg.VerifyVetoHz > 0 && r.healthyAt > 0 && r.symIdx-r.healthyAt <= int64(6*r.cfg.Baud) {
		return
	}
	r.setF0(f0, true)
	r.Changes[len(r.Changes)-1].Verify = true
	r.unlockedBl = 0
}
