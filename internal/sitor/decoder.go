package sitor

import (
	"fmt"
	"strings"

	"navtex/internal/fsk"
)

// Config holds the framing settings; DefaultConfig gives the reference values.
type Config struct {
	Framing         Framing    `json:"framing" doc:"How the character alignment is found. hard: ten valid 4-of-7 words in a row lock the framing (fast, but needs a clean signal). soft: scores all alignments from the 35-bit repetition of each character and locks on the accumulated evidence (works in more noise, follows bit slips and station changes, but locks a few seconds later and shows characters it cannot trust as ~ or !). The soft framing reads the bit order as LSB first, ignores unshift_on_space, and leaves lock_words, unlock_bad_words, parity_votes, parity_factor, keep_parity_symbols and relock_words unused. Changing it restarts the channel" choices:"hard,soft"`
	Order           Order      `json:"bit_order" doc:"Which bit of each 7-bit code is sent first. auto: decide by finding ZCZC (LSB is shown until then)" choices:"auto,lsb,msb"`
	Polarity        Polarity   `json:"polarity" doc:"Which tone is the mark: normal (receiver in USB, mark is the higher tone), inverted (LSB) or auto (try both)" choices:"auto,normal,inverted"`
	UnshiftOnSpace  bool       `json:"unshift_on_space" doc:"A space character returns to the letters shift (helps when a shift character was missed)"`
	Unreadable      Unreadable `json:"unreadable" doc:"What to print when both copies of a character are unreadable: the nearest valid character, * or ~ (as YaND prints it)" choices:"nearest,star,tilde"`
	BreakBeforeZCZC bool       `json:"break_before_zczc" doc:"Always start ZCZC at the beginning of a line (inserts a line break if needed)"`
	BreakAfterBRK   bool       `json:"break_after_brk" doc:"Insert a line break after the station's BRK marker"`
	ShiftHz         float64    `json:"shift_hz" unit:"Hz" doc:"Mark/space frequency spacing; soft values are normalised by half of this"`
	MinRun          int        `json:"lock_words" doc:"Consecutive valid 4-of-7 words needed to lock word framing"`
	MaxBad          int        `json:"unlock_bad_words" doc:"Consecutive invalid words that end a framing lock"`
	VoteAgree       int        `json:"parity_votes" doc:"Agreeing DX/RX pairs needed to decide which slots carry the first copy"`
	VoteFactor      int        `json:"parity_factor" doc:"... and this many times more than the other parity"`
	KeepParity      float64    `json:"keep_parity_symbols" unit:"symbols" doc:"A re-lock at the same alignment within this many symbols reuses the DX/RX parity (0 = never)"`
	RelockWords     int        `json:"relock_words" doc:"Weak signals: after a framing lock ended on invalid words, lock again at the same polarity and alignment after this many valid words in a row (the bit clock goes on through a fade, so the alignment is still right); 0 = only lock_words counts"`
	RelockWindowS   float64    `json:"relock_window_s" unit:"s" doc:"... for this long after the lock ended; the DX/RX parity is reused as well"`
}

func DefaultConfig() Config {
	return Config{Order: Auto, Polarity: PolAuto, Unreadable: Tilde, BreakBeforeZCZC: true, BreakAfterBRK: true,
		ShiftHz: 170, MinRun: 10, MaxBad: 8, VoteAgree: 10, VoteFactor: 3, KeepParity: 3000, RelockWords: 4, RelockWindowS: 120}
}

// EventKind identifies a decoder event.
type EventKind int

const (
	EvLock   EventKind = iota // framing lock; Pol and Align are set
	EvUnlock                  // framing lost
	EvOrder                   // bit order decided; Order is set
	EvText                    // decoded text in Text
)

// Event is something the decoder reports.
type Event struct {
	Kind  EventKind
	Text  string
	Sym   int64 // EvText: index of the first symbol of the character's (DX) word; time = Sym / baud since stream start
	Pol   int   // +1: mark is the higher tone, -1: lower
	Align int   // symbol alignment (0..6) of the word framing
	Order Order
	// EvUnlock: how long the lock lasted and how it ended.
	Syms   int64  // symbols locked
	Words  int    // words seen while locked
	Bad    int    // invalid words among them
	Reason string // "bad words" (lost the signal), "resync" or "end"
	// EvLock: true when this is a quick re-lock after a fade.
	Relock bool
}

func (e Event) String() string {
	switch e.Kind {
	case EvLock:
		if e.Relock {
			return fmt.Sprintf("lock pol=%+d align=%d (re-lock after fade)", e.Pol, e.Align)
		}
		return fmt.Sprintf("lock pol=%+d align=%d", e.Pol, e.Align)
	case EvUnlock:
		if e.Reason == "" {
			return "unlock"
		}
		return fmt.Sprintf("unlock (%s) after %.1f s, %d words, %d invalid", e.Reason, float64(e.Syms)/100, e.Words, e.Bad)
	case EvOrder:
		return "bit order " + e.Order.String()
	}
	return fmt.Sprintf("text %q @%d", e.Text, e.Sym)
}

type word struct {
	s    [7]float64 // normalised soft values, signed so that + = mark
	hard uint8      // bit t set when s[t] > 0
	ok   bool       // exactly four marks
}

func weight(m uint8) int {
	n := 0
	for ; m != 0; m &= m - 1 {
		n++
	}
	return n
}

// Decoder is the streaming SITOR-B decoder: framing lock (both polarities tried in parallel), DX/RX parity vote,
// soft-decision combining of each character with its repeat 5 slots later, bit-order detection and text output.
type Decoder struct {
	cfg Config

	idx  int64 // symbols consumed
	hist []float64
	run  [2][7]int    // consecutive valid words per polarity (0: +1, 1: -1) and alignment
	agr  [2][7]uint64 // per polarity and alignment: bit n set when the word n words ago repeated its predecessor 5 words earlier
	agrL uint64       // the same for the locked framing

	locked   bool
	pol      int
	align    int
	words    []word // words not yet fully consumed; words[0] has global index base
	base     int64  // word index (k = (j-align)/7) of words[0]
	nextEmit int64  // next slot to consider for output
	bad      int
	lockIdx  int64 // symbol index at the lock
	nWords   int   // words since the lock
	nBad     int   // invalid words since the lock
	agree    [2]int
	dxKnown  bool
	dxPar    int64    // DX slots are those with k%2 == dxPar
	last     struct { // the framing lock that ended last (for a quick re-lock)
		ok         bool
		pol, align int
		symIdx     int64
	}
	lastPar struct {
		ok     bool
		align  int
		par    int64
		symIdx int64
	}

	order   Order // committed order (Auto until decided)
	interps [2]*interp
	tails   [2]string  // last characters per candidate order while undecided (ZCZC detection)
	msbBuf  []textItem // MSB reading since lock while undecided (released if MSB turns out to be right)
	events  []Event
	shape   shaper
}

func NewDecoder(cfg Config) *Decoder {
	d := &Decoder{cfg: cfg, order: cfg.Order}
	d.shape.cfg, d.shape.events = &d.cfg, &d.events
	return d
}

// Order returns the committed bit order (Auto while undecided).
func (d *Decoder) Order() Order { return d.order }

// Locked reports whether the framing is locked.
func (d *Decoder) Locked() bool { return d.locked }

// SetPolarity changes which mark direction may be locked; a lock in the wrong direction is dropped.
func (d *Decoder) SetPolarity(p Polarity) []Event {
	d.cfg.Polarity = p
	if d.locked && !p.allows(d.pol) {
		return d.Resync()
	}
	d.events = d.events[:0]
	return d.events
}

// SetConfig applies a changed configuration to a running decoder. Every option takes effect from the next
// decision; the mark/space spacing (ShiftHz) is not changed. A forced bit order drops the framing lock so that
// the next lock starts with it; polarity may drop a lock in the wrong direction (see SetPolarity).
func (d *Decoder) SetConfig(c Config) []Event {
	c.ShiftHz = d.cfg.ShiftHz
	orderChanged := c.Order != d.cfg.Order
	pol, uos := c.Polarity, c.UnshiftOnSpace
	d.cfg = c
	d.SetUnshiftOnSpace(uos)
	evs := d.SetPolarity(pol)
	if orderChanged {
		d.order = c.Order
		evs = d.Resync()
	}
	return evs
}

// SetUnshiftOnSpace changes the option on the fly.
func (d *Decoder) SetUnshiftOnSpace(on bool) {
	d.cfg.UnshiftOnSpace = on
	for _, ip := range d.interps {
		if ip != nil {
			ip.uos = on
		}
	}
}

// Resync drops the framing lock (if any) and forgets the word-run history, so the next lock needs a fresh run of valid
// words. The bit order and DX/RX parity memory are kept unless the framing was lost for a long time.
func (d *Decoder) Resync() []Event {
	d.events = d.events[:0]
	if d.locked {
		d.emitReady(false)
		d.unlock("resync")
	}
	d.run = [2][7]int{}
	d.last.ok = false
	d.lastPar.ok = false
	return d.events
}

// Process consumes symbols and returns the events they produced (valid until the next call).
func (d *Decoder) Process(syms []fsk.Symbol) []Event {
	d.events = d.events[:0]
	for _, s := range syms {
		d.push(s.Soft)
	}
	return d.events
}

// Flush ends the stream: releases pending characters.
func (d *Decoder) Flush() []Event {
	d.events = d.events[:0]
	if d.locked {
		d.emitReady(true)
		d.unlock("end")
	}
	d.shape.flush()
	return d.events
}

func (d *Decoder) norm(pol int, raw float64) float64 {
	v := float64(pol) * raw / (d.cfg.ShiftHz / 2)
	if v > 1.5 {
		return 1.5
	}
	if v < -1.5 {
		return -1.5
	}
	return v
}

func (d *Decoder) mkWord(pol int, raw []float64) word {
	var w word
	for t := 0; t < 7; t++ {
		w.s[t] = d.norm(pol, raw[t])
		if w.s[t] > 0 {
			w.hard |= 1 << t
		}
	}
	w.ok = weight(w.hard) == 4
	return w
}

func (d *Decoder) push(soft float64) {
	d.hist = append(d.hist, soft)
	if keep := 7*d.cfg.MinRun + 7; len(d.hist) > keep {
		d.hist = append(d.hist[:0], d.hist[len(d.hist)-keep:]...)
	}
	i := d.idx
	d.idx++
	if i < 6 {
		return
	}
	j := i - 6
	a := int(j % 7)
	raw := d.hist[len(d.hist)-7:]
	// validity per polarity for the word starting at j
	for pi, pol := range [2]int{1, -1} {
		n := 0
		for _, v := range raw {
			if float64(pol)*v > 0 {
				n++
			}
		}
		if n == 4 {
			d.run[pi][a]++
		} else {
			d.run[pi][a] = 0
		}
	}
	if d.locked {
		if a == d.align {
			d.addWord(d.mkWord(d.pol, raw))
		}
		return
	}
	for pi, pol := range [2]int{1, -1} {
		if d.run[pi][a] >= d.cfg.MinRun && d.cfg.Polarity.allows(pol) {
			d.lock(pol, a, j, d.cfg.MinRun, false)
			return
		}
	}
	// a fade does not move the bit clock: the framing that was lost a moment ago is still the likely one
	if l := d.cfg.RelockWords; l > 0 && d.last.ok && a == d.last.align && float64(d.idx-d.last.symIdx) < d.cfg.RelockWindowS*100 {
		pi := 0
		if d.last.pol < 0 {
			pi = 1
		}
		if d.run[pi][a] >= min(l, d.cfg.MinRun) && d.cfg.Polarity.allows(d.last.pol) {
			d.lock(d.last.pol, a, j, min(l, d.cfg.MinRun), true)
		}
	}
}

func (d *Decoder) lock(pol, a int, j int64, nw int, sticky bool) {
	d.locked, d.pol, d.align = true, pol, a
	d.bad, d.nWords, d.nBad, d.lockIdx = 0, 0, 0, d.idx
	d.agree = [2]int{}
	d.dxKnown = false
	d.words = d.words[:0]
	first := j - 7*int64(nw-1)
	d.base = (first - int64(a)) / 7
	d.nextEmit = d.base
	d.interps = [2]*interp{{order: LSB, uos: d.cfg.UnshiftOnSpace}, {order: MSB, uos: d.cfg.UnshiftOnSpace}}
	d.tails, d.msbBuf = [2]string{}, nil
	d.shape.reset()
	if d.order != Auto {
		d.interps[0] = &interp{order: d.order, uos: d.cfg.UnshiftOnSpace}
		d.interps[1] = nil
	}
	if lp := d.lastPar; lp.ok && lp.align == a && (d.cfg.KeepParity > 0 && float64(d.idx-lp.symIdx) < d.cfg.KeepParity || sticky) {
		d.dxKnown, d.dxPar = true, lp.par
	}
	d.events = append(d.events, Event{Kind: EvLock, Pol: pol, Align: a, Relock: sticky})
	// rebuild the words that triggered the lock from the history (oldest first)
	h := d.hist
	for w := 0; w < nw; w++ {
		start := len(h) - 7 - 7*(nw-1-w)
		d.addWord(d.mkWord(pol, h[start:start+7]))
	}
}

func (d *Decoder) unlock(reason string) {
	d.last.ok, d.last.pol, d.last.align, d.last.symIdx = true, d.pol, d.align, d.idx
	d.shape.flush()
	d.locked = false
	d.words = d.words[:0]
	d.events = append(d.events, Event{Kind: EvUnlock, Reason: reason, Syms: d.idx - d.lockIdx, Words: d.nWords, Bad: d.nBad})
}

func (d *Decoder) addWord(w word) {
	d.words = append(d.words, w)
	k := d.base + int64(len(d.words)) - 1
	d.nWords++
	if w.ok {
		d.bad = 0
	} else {
		d.bad++
		d.nBad++
	}
	if !d.dxKnown {
		if pk := k - 5; pk >= d.base {
			if p := d.words[pk-d.base]; p.ok && w.ok && p.hard == w.hard {
				d.agree[pk&1]++
			}
			cfg := d.cfg
			for par := int64(0); par < 2; par++ {
				if d.agree[par] >= cfg.VoteAgree && d.agree[par] >= cfg.VoteFactor*d.agree[1-par] {
					d.dxKnown, d.dxPar = true, par
					d.lastPar.ok, d.lastPar.align, d.lastPar.par = true, d.align, par
				}
			}
		}
	}
	if !d.dxKnown && len(d.words) > 2000 { // no DX/RX evidence for ~2.5 minutes: do not grow without bound
		d.words = append(d.words[:0], d.words[1000:]...)
		d.base += 1000
		d.nextEmit = d.base
	}
	d.emitReady(false)
	if d.bad >= d.cfg.MaxBad {
		d.emitReady(true)
		d.unlock("lost signal")
	}
	if d.locked {
		d.lastPar.symIdx = d.idx
	}
}

// emitReady decodes DX slots whose repeat has arrived (or all remaining ones when final).
func (d *Decoder) emitReady(final bool) {
	if !d.dxKnown {
		if final {
			d.words = d.words[:0]
		}
		return
	}
	end := d.base + int64(len(d.words))
	for d.nextEmit < end {
		k := d.nextEmit
		if k&1 != d.dxPar {
			d.nextEmit++
			continue
		}
		if k+5 >= end && !final {
			break
		}
		dx := d.words[k-d.base]
		var rx *word
		if k+5 < end {
			rx = &d.words[k+5-d.base]
		}
		d.decodeSlot(dx, rx, k)
		d.nextEmit++
	}
	// drop words no longer needed
	if drop := int(d.nextEmit - d.base); drop > 0 && drop <= len(d.words) {
		d.words = append(d.words[:0], d.words[drop:]...)
		d.base = d.nextEmit
	}
}

// decodeSlot combines the DX word with its repeat by maximum likelihood over the 35 codewords.
func (d *Decoder) decodeSlot(dx word, rx *word, k int64) {
	var v [7]float64
	for t := 0; t < 7; t++ {
		v[t] = dx.s[t]
		if rx != nil {
			v[t] += rx.s[t]
		}
	}
	best, bestScore, first := uint8(0), 0.0, true
	for _, m := range validPatterns {
		var sc float64
		for t := 0; t < 7; t++ {
			if m>>t&1 == 1 {
				sc += v[t]
			} else {
				sc -= v[t]
			}
		}
		if first || sc > bestScore {
			best, bestScore, first = m, sc, false
		}
	}
	// two different valid words are two different signals (phasing), not repeats: keep the DX word
	if rx != nil && dx.ok && rx.ok && dx.hard != rx.hard {
		best = dx.hard
	}
	unreadable := !dx.ok && (rx == nil || !rx.ok)
	d.text(best, k*7+int64(d.align), unreadable)
}

// text interprets one decoded character. While the bit order is undecided the LSB reading (the M.476 default) is shown
// immediately, and both readings are watched for "ZCZC"; if only the MSB reading finds it, its buffered text is released.
func (d *Decoder) text(mask uint8, sym int64, unreadable bool) {
	pol := d.cfg.Unreadable
	if d.order != Auto {
		if t := d.interps[0].feed(mask, unreadable, pol); t != "" {
			d.shape.put(t, sym)
		}
		return
	}
	var t [2]string
	for i, p := range d.interps {
		t[i] = p.feed(mask, unreadable, pol)
	}
	if t[0] != "" {
		d.shape.put(t[0], sym)
	}
	if t[1] != "" {
		d.msbBuf = append(d.msbBuf, textItem{t[1], sym})
		if len(d.msbBuf) > 4096 {
			d.msbBuf = d.msbBuf[len(d.msbBuf)-64:]
		}
	}
	for i := range d.tails {
		joined := d.tails[i] + t[i]
		if strings.Contains(joined, "ZCZC") {
			d.order = d.interps[i].order
			d.events = append(d.events, Event{Kind: EvOrder, Order: d.order})
			if i == 1 {
				d.shape.reset() // drop the LSB reading's line state; the MSB text replaces it
				for _, it := range d.msbBuf {
					d.shape.put(it.text, it.sym)
				}
			}
			d.interps = [2]*interp{d.interps[i], nil}
			d.tails, d.msbBuf = [2]string{}, nil
			return
		}
		if len(joined) > 3 {
			joined = joined[len(joined)-3:]
		}
		d.tails[i] = joined
	}
}
