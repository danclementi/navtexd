package sitor

import (
	"navtex/internal/fsk"
	"navtex/internal/softlock"
)

// Framer is what the engine needs from a framing and character decoder: the hard word framing (Decoder) or the soft
// repetition lock (SoftDecoder).
type Framer interface {
	Process(syms []fsk.Symbol) []Event
	Flush() []Event
	Resync() []Event
	SetPolarity(p Polarity) []Event
	SetConfig(c Config) []Event
	SetUnshiftOnSpace(on bool)
	Order() Order
	Locked() bool
}

// New makes the decoder selected by cfg.Framing.
func New(cfg Config) Framer {
	if cfg.Framing == FramingSoft {
		return NewSoftDecoder(cfg)
	}
	return NewDecoder(cfg)
}

// SoftDecoder is the streaming decoder with the soft repetition lock (package softlock) in place of the hard word
// framing. It reads the code as LSB first (the ITU-R M.476 order), tries both polarities at once unless one is forced,
// and prints characters it cannot trust as the unreadable mark (or "!" where the two copies each look valid but
// differ).
type SoftDecoder struct {
	cfg      Config
	lk       *softlock.Lock
	idx      int64 // symbols consumed
	off      int64 // symbol count when the current lock object was made (bit numbers restart there)
	locked   bool
	pol      int
	phase    int
	lockIdx  int64
	orderOut bool
	events   []Event
	shape    shaper
}

// NewSoftDecoder makes a soft-lock decoder.
func NewSoftDecoder(cfg Config) *SoftDecoder {
	d := &SoftDecoder{cfg: cfg}
	d.shape.cfg, d.shape.events = &d.cfg, &d.events
	d.lk = softlock.New(d.params())
	return d
}

func (d *SoftDecoder) params() softlock.Params {
	p := softlock.DefaultParams()
	switch d.cfg.Polarity {
	case PolNormal:
		p.Polarity = 1
	case PolInverted:
		p.Polarity = -1
	default:
		p.Polarity = 0
	}
	return p
}

// Order is LSB: the soft framing does not detect the bit order.
func (d *SoftDecoder) Order() Order { return LSB }

// Locked reports whether the lock is held.
func (d *SoftDecoder) Locked() bool { return d.locked }

// SetUnshiftOnSpace is accepted for compatibility; the soft framing does not use it.
func (d *SoftDecoder) SetUnshiftOnSpace(on bool) { d.cfg.UnshiftOnSpace = on }

// SetPolarity changes which polarity may lock; the search starts again.
func (d *SoftDecoder) SetPolarity(p Polarity) []Event {
	if p == d.cfg.Polarity {
		d.events = d.events[:0]
		return d.events
	}
	d.cfg.Polarity = p
	return d.Resync()
}

// SetConfig applies a changed configuration. The mark/space spacing (ShiftHz) is not changed, and the polarity changes
// only through SetPolarity. (The soft lock has no settings of its own yet.)
func (d *SoftDecoder) SetConfig(c Config) []Event {
	c.ShiftHz, c.Polarity, c.Framing = d.cfg.ShiftHz, d.cfg.Polarity, FramingSoft
	d.cfg = c
	d.events = d.events[:0]
	return d.events
}

// Resync drops the lock and starts the search again from the next symbol.
func (d *SoftDecoder) Resync() []Event {
	d.events = d.events[:0]
	d.finish("resync")
	d.lk = softlock.New(d.params())
	d.off = d.idx // the new lock numbers its decisions from 0
	return d.events
}

// Process consumes symbols and returns the events they produced (valid until the next call).
func (d *SoftDecoder) Process(syms []fsk.Symbol) []Event {
	d.events = d.events[:0]
	for _, s := range syms {
		d.push(s.Soft)
	}
	return d.events
}

// Flush ends the stream: releases pending characters.
func (d *SoftDecoder) Flush() []Event {
	d.events = d.events[:0]
	d.finish("end")
	return d.events
}

// finish emits what the lock still holds and reports the unlock.
func (d *SoftDecoder) finish(reason string) {
	for _, c := range d.lk.Finish() {
		d.char(c)
	}
	if d.locked {
		d.unlocked(reason)
	}
	d.shape.flush()
}

func (d *SoftDecoder) unlocked(reason string) {
	d.locked = false
	syms := d.idx - d.lockIdx
	d.events = append(d.events, Event{Kind: EvUnlock, Reason: reason, Syms: syms, Words: int(syms / 7)})
	d.shape.flush()
}

// norm scales a soft value by half the tone spacing and limits it, as the hard framing does.
func (d *SoftDecoder) norm(raw float64) float64 {
	v := raw / (d.cfg.ShiftHz / 2)
	if v > 1.5 {
		return 1.5
	}
	if v < -1.5 {
		return -1.5
	}
	return v
}

func (d *SoftDecoder) push(raw float64) {
	d.idx++
	chars := d.lk.Push(d.norm(raw))
	now := d.lk.Locked()
	if now {
		pol, phase := d.lk.Polarity(), d.lk.Phase()
		if !d.locked {
			d.lockIdx = d.idx
			if !d.orderOut {
				d.orderOut = true
				d.events = append(d.events, Event{Kind: EvOrder, Order: LSB})
			}
			d.events = append(d.events, Event{Kind: EvLock, Pol: pol, Align: phase})
		} else if pol != d.pol || phase != d.phase {
			d.events = append(d.events, Event{Kind: EvLock, Pol: pol, Align: phase, Relock: true})
		}
		d.locked, d.pol, d.phase = true, pol, phase
	}
	for _, c := range chars {
		d.char(c)
	}
	if !now && d.locked {
		d.unlocked("lost signal")
	}
}

// char turns one decoded character into text.
func (d *SoftDecoder) char(c softlock.Char) {
	var t string
	switch c.Ch {
	case '~':
		t = d.cfg.Unreadable.Mark()
		if t == "" {
			t = "~"
		}
	case '\a':
		return // the bell (J in figures) is not printed
	default:
		t = string(c.Ch)
	}
	sym := d.off + int64(c.BitIndex) - 35 // the time of the first copy of the character
	if sym < d.off {
		sym = d.off
	}
	d.shape.put(t, sym)
}
