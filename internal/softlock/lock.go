// Package softlock is a soft, repetition-based alignment lock for SITOR-B (NAVTEX) bit streams. It finds the character
// alignment from the 35-bit DX/RX repetition using soft symbol values, so it locks in noise where hard word framing
// cannot, repairs one-bit slips, follows station changes, and reports characters it could not trust.
//
// It is a port of navtex-core/src/{softlock,score,combine,charset}.rs of navtex-rs (https://github.com/bgm60/navtex-rs,
// by bgm60), which is GPL-3.0-or-later, as is this program. See docs/soft-lock-design.md.
package softlock

import "math"

// historyMax bounds the soft values kept; when more are stored the oldest are dropped until half this many remain.
const historyMax = 6000

// Char is one decoded character.
type Char struct {
	Ch rune
	// BitIndex is the number of the first bit decision of the repeat group that produced the character, counting from
	// the start of the stream.
	BitIndex int
	// Releases is how many locks had been released before this character came out. The characters of one lock (one
	// message) share a number; the next lock's characters have a higher one, so a caller can start a new line.
	Releases int
}

type pending struct {
	end      int // the decision just after the repeat group; released once the stream is far enough beyond it
	ch       rune
	bitIndex int
}

// Lock is the streaming lock.
type Lock struct {
	p       Params
	signs   []float64 // one per searched polarity block
	nHyp    int
	history []float64
	base    int
	count   int
	nextBin int
	scores  *scoreWindow
	rowBuf  []float64
	zBuf    []float64

	locked     bool
	h          int
	neighbours [2]int
	lowRun     int
	ownLowRun  int
	challenge  int
	dwell      int

	releases    int
	ownReleases int
	switches    int

	runChar     rune
	runLen      int
	runDropping bool

	ghostRun      int
	ghostKeys     []int
	ghostDropping bool

	letterRunDrops   int
	ghostRunsDropped int

	prevEnd    int
	groupStart int
	firstGroup int
	groupsDone int
	recent     [][7]float64
	comb       combiner
	queue      []pending
	emitted    int
}

// New makes a lock that is searching.
func New(p Params) *Lock {
	l := &Lock{p: p}
	switch p.Polarity {
	case -1:
		l.signs = []float64{-1}
	case 0:
		l.signs = []float64{1, -1}
	default:
		l.signs = []float64{1}
	}
	l.nHyp = perPolarity * len(l.signs)
	l.scores = newScoreWindow(p.Window, l.nHyp)
	l.rowBuf = make([]float64, l.nHyp)
	l.zBuf = make([]float64, l.nHyp)
	l.comb = newCombiner()
	return l
}

// slipNeighbours are the two hypotheses reached from h by slipping one bit later or earlier (within its polarity);
// slipping past either end of the 7 phases wraps round and swaps the parity.
func slipNeighbours(h int) (int, int) {
	blk := h / perPolarity * perPolarity
	local := h % perPolarity
	phase, parity := local/2, local%2
	lp, lq := phase+1, parity
	if phase == 6 {
		lp, lq = 0, 1-parity
	}
	ep, eq := phase-1, parity
	if phase == 0 {
		ep, eq = 6, 1-parity
	}
	return blk + lp*2 + lq, blk + ep*2 + eq
}

// floorDiv is the division that rounds toward minus infinity (b > 0).
func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// ceilDiv divides rounding up, also for negative numbers: ceilDiv(-3, 7) is 0.
func ceilDiv(a, b int) int { return -floorDiv(-a, b) }

func rotate(c Code, k uint) Code { return ((c << k) | (c >> (7 - k))) & 0x7F }

func hamming(a, b Code) int { return weight(a ^ b) }

// isIdleLookalike: are the two groups each within one bit of an idle pair (the two phasing signals, one after the
// other) read 1 to 6 bits out of step? Two copies of a real character agree, so real text cannot look like this.
func isIdleLookalike(dx, rx Code) bool {
	for k := uint(1); k <= 6; k++ {
		for _, pr := range [2][2]Code{{Phasing1, Phasing2}, {Phasing2, Phasing1}} {
			if hamming(dx, rotate(pr[0], k)) <= 1 && hamming(rx, rotate(pr[1], k)) <= 1 {
				return true
			}
		}
	}
	return false
}

// firstMax is the index of the largest value; the first one if several are equal.
func firstMax(v []float64) int {
	best := 0
	for i, x := range v {
		if x > v[best] {
			best = i
		}
	}
	return best
}

// bestSplit: where did the signal change from alignment old to alignment new? The inputs are the two alignments'
// scores over the same comparisons, oldest first. It returns k, the number of leading comparisons that belong to the
// old alignment: the k that maximises the old scores before it plus the new scores after it (the earliest if equal).
func bestSplit(old, nw []float64) int {
	m := len(old)
	oldTot := make([]float64, m+1)
	newTot := make([]float64, m+1)
	for j := 0; j < m; j++ {
		oldTot[j+1] = oldTot[j] + old[j]
		newTot[j+1] = newTot[j] + nw[j]
	}
	fits := make([]float64, m+1)
	for j := range fits {
		fits[j] = oldTot[j] + (newTot[m] - newTot[j])
	}
	return firstMax(fits)
}

// Push feeds in the soft value of the next bit decision (positive = mark = 1) and returns the characters that become
// ready, which is usually none.
func (l *Lock) Push(soft float64) []Char {
	l.history = append(l.history, soft)
	l.count++
	var out []Char
	for binsAvailable(l.count) > l.nextBin {
		out = append(out, l.step(l.nextBin)...)
		l.nextBin++
	}
	if len(l.history) > historyMax {
		drop := len(l.history) - historyMax/2
		l.history = append(l.history[:0], l.history[drop:]...)
		l.base += drop
	}
	l.emitted += len(out)
	return out
}

// Finish ends the stream and returns what is still held back.
func (l *Lock) Finish() []Char {
	if l.locked {
		l.advanceGroups()
	}
	var out []Char
	for _, it := range l.queue {
		out = append(out, Char{it.ch, it.bitIndex, l.releases})
	}
	l.queue = l.queue[:0]
	l.emitted += len(out)
	l.locked = false
	return out
}

// Reset forgets everything and searches again from the next decision (the bit numbering restarts at zero).
func (l *Lock) Reset() {
	*l = *New(l.p)
}

func (l *Lock) Locked() bool { return l.locked }

// Hypothesis is the locked hypothesis (14*polarity block + 2*bit phase + parity), or -1.
func (l *Lock) Hypothesis() int {
	if !l.locked {
		return -1
	}
	return l.h
}

// Polarity is +1 or -1 for the polarity of the lock (0 when not locked).
func (l *Lock) Polarity() int {
	if !l.locked {
		return 0
	}
	return int(l.signs[l.h/perPolarity])
}

// Phase is the bit phase (0..6) of the lock.
func (l *Lock) Phase() int { return (l.h % perPolarity) / 2 }

func (l *Lock) Releases() int         { return l.releases }
func (l *Lock) OwnReleases() int      { return l.ownReleases }
func (l *Lock) Switches() int         { return l.switches }
func (l *Lock) Comparisons() int      { return l.nextBin }
func (l *Lock) Pending() int          { return len(l.queue) }
func (l *Lock) Emitted() int          { return l.emitted }
func (l *Lock) LetterRunDrops() int   { return l.letterRunDrops }
func (l *Lock) GhostRunsDropped() int { return l.ghostRunsDropped }

// step scores comparison b, then locks, or decodes and checks whether to release.
func (l *Lock) step(b int) []Char {
	binScores(l.history, l.base, b, l.signs, &l.p, l.rowBuf)
	l.scores.push(l.rowBuf)
	if l.locked {
		l.advanceGroups()
		return l.maybeRelease(b)
	}
	if !l.scores.full() {
		return nil
	}
	z := l.zBuf
	l.scores.z(&l.p, z)
	top := 0
	for h := 1; h < l.nHyp; h++ {
		if z[h] > z[top] {
			top = h
		}
	}
	second := math.Inf(-1)
	for h := 0; h < l.nHyp; h++ {
		if h != top && z[h] > second {
			second = z[h]
		}
	}
	if z[top] >= l.p.ZStar && z[top]-second >= l.p.Gap {
		l.lock(top, b)
	}
	return nil
}

func (l *Lock) sign() float64 { return l.signs[l.h/perPolarity] }

// alignGroups sets the first group to read: the first that starts at hypothesis c's bit phase on or after from.
func (l *Lock) alignGroups(c, from int) {
	phase := (c % perPolarity) / 2
	first := ceilDiv(from-phase, 7)
	if first < 0 {
		first = 0
	}
	start := phase + 7*first
	for start < l.base { // never start before the history that was kept
		first++
		start += 7
	}
	l.firstGroup, l.groupStart, l.groupsDone = first, start, 0
}

// lock locks to hypothesis h, found at comparison b, and decodes everything from just before the detection window.
func (l *Lock) lock(h, b int) {
	l.locked = true
	l.h = h
	l.neighbours[0], l.neighbours[1] = slipNeighbours(h)
	l.lowRun, l.ownLowRun, l.challenge, l.dwell = 0, 0, 0, 0
	l.resetRun()
	l.resetGhost()
	l.comb = newCombiner()
	windowStart := (b - l.p.Window + 1 - l.p.ReplayMargin) * binsPerStep
	from := max(windowStart, l.prevEnd, l.base)
	l.alignGroups(h, from)
	l.recent = l.recent[:0]
	l.queue = l.queue[:0]
	l.advanceGroups()
}

// advanceGroups reads every 7-bit group that has completed since the last call. A group that is a repeat copy is
// combined with the group five before it, and any character that results is queued.
func (l *Lock) advanceGroups() {
	parity := (l.h % perPolarity) % 2
	sign := l.sign()
	for l.groupStart+7*l.groupsDone+7 <= l.count {
		start := l.groupStart + 7*l.groupsDone
		soft := group(l.history, l.base, start, sign)
		if len(l.recent) == 6 {
			copy(l.recent, l.recent[1:])
			l.recent = l.recent[:5]
		}
		l.recent = append(l.recent, soft)
		isRepeat := (l.firstGroup+l.groupsDone)%2 == parity
		if l.groupsDone >= 5 && isRepeat {
			first := l.recent[0] // five groups before this one
			dx, rx := hardCode(&first), hardCode(&soft)
			if l.skipIdleLookalike(dx, rx, start) {
				l.groupsDone++
				continue
			}
			if ch, ok := l.comb.combine(&first, &soft); ok {
				l.enqueue(start+7, ch, start)
			}
		}
		l.groupsDone++
	}
}

func (l *Lock) resetRun() { l.runChar, l.runLen, l.runDropping = 0, 0, false }

func (l *Lock) resetGhost() {
	l.ghostRun = 0
	l.ghostKeys = l.ghostKeys[:0]
	l.ghostDropping = false
}

// skipIdleLookalike tracks idle look-alike pairs and reports whether the pair is to be dropped without decoding. The
// first GhostHold-1 of a run are decoded as usual (they cannot yet be told from text); when the run reaches GhostHold,
// the characters they produced, still held back, are removed too.
func (l *Lock) skipIdleLookalike(dx, rx Code, key int) bool {
	hold := l.p.GhostHold
	if hold == 0 {
		return false
	}
	if isPhasing(dx) && isPhasing(rx) {
		return false // a genuine idle pair: the combiner prints nothing for it
	}
	if !isIdleLookalike(dx, rx) {
		l.resetGhost()
		return false
	}
	l.ghostRun++
	if l.ghostRun < hold {
		l.ghostKeys = append(l.ghostKeys, key)
		return false
	}
	if !l.ghostDropping {
		l.ghostDropping = true
		l.ghostRunsDropped++
		earlier := l.ghostKeys
		l.ghostKeys = nil
		kept := l.queue[:0]
		for _, it := range l.queue {
			drop := false
			for _, k := range earlier {
				if k == it.bitIndex {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, it)
			}
		}
		l.queue = kept
	}
	return true
}

func isAlpha(r rune) bool { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }

// enqueue adds a decoded character to the held-back queue, dropping runs of MaxLetterRun or more identical letters.
// Characters are held back longer than the run limit, so the start of a run is still in the queue when it is
// recognised.
func (l *Lock) enqueue(end int, ch rune, bitIndex int) {
	limit := l.p.MaxLetterRun
	if limit == 0 || !isAlpha(ch) {
		l.resetRun()
		l.queue = append(l.queue, pending{end, ch, bitIndex})
		return
	}
	if l.runChar == ch {
		l.runLen++
	} else {
		l.runChar, l.runLen, l.runDropping = ch, 1, false
	}
	if l.runDropping {
		return
	}
	if l.runLen >= limit {
		l.runDropping = true
		l.letterRunDrops++
		for i := 0; i < l.runLen-1; i++ {
			if n := len(l.queue); n > 0 && l.queue[n-1].ch == ch {
				l.queue = l.queue[:n-1]
			}
		}
		return
	}
	l.queue = append(l.queue, pending{end, ch, bitIndex})
}

// maybeRelease releases the characters whose delay has passed, then drops the lock if the locked alignment, or either
// of its one-bit-slip neighbours, has scored below ReleaseZ for ReleaseHold comparisons in a row.
func (l *Lock) maybeRelease(b int) []Char {
	if l.p.Follow {
		l.maybeSwitch(b)
	}
	p := &l.p
	limit := binsPerStep*(b-p.OutputDelay) + binsPerStep
	z := l.zBuf
	l.scores.zLast(p.ReleaseWindow, p, z)
	best := math.Max(z[l.h], math.Max(z[l.neighbours[0]], z[l.neighbours[1]]))
	if best < p.ReleaseZ {
		l.lowRun++
	} else {
		l.lowRun = 0
	}
	if z[l.h] < p.ReleaseZ {
		l.ownLowRun++
	} else {
		l.ownLowRun = 0
	}
	var out []Char
	n := 0
	for n < len(l.queue) && l.queue[n].end <= limit {
		it := l.queue[n]
		out = append(out, Char{it.ch, it.bitIndex, l.releases}) // counted before this step's release
		n++
	}
	l.queue = append(l.queue[:0], l.queue[n:]...)
	if l.lowRun >= p.ReleaseHold || (p.Follow && l.ownLowRun >= p.OwnReleaseHold) {
		l.queue = l.queue[:0] // what is left is the stray tail
		l.locked = false
		l.releases++
		if l.lowRun < p.ReleaseHold {
			l.ownReleases++
		}
		l.prevEnd = max(l.prevEnd, limit)
		l.scores.clear()
	}
	return out
}

// maybeSwitch moves the lock to another alignment that is clearly better.
func (l *Lock) maybeSwitch(b int) {
	p := &l.p
	s := p.SwitchWindow
	l.dwell++
	if l.scores.len() < s || l.dwell < s {
		l.challenge = 0
		return
	}
	z := l.zBuf
	l.scores.zLast(s, p, z)
	c := firstMax(z)
	if c == l.h {
		l.challenge = 0
		return
	}
	near := c == l.neighbours[0] || c == l.neighbours[1]
	needZ, needMargin := p.SwitchZ, p.SwitchMargin
	if near {
		needZ, needMargin = p.SlipZ, p.SlipMargin
	}
	if z[c] >= needZ && z[c]-z[l.h] >= needMargin {
		l.challenge++
	} else {
		l.challenge = 0
	}
	if l.challenge >= p.SwitchHold {
		l.switchTo(c, b, near)
	}
}

// switchTo re-aligns to hypothesis c from the point where the stored scores say the signal changed.
func (l *Lock) switchTo(c, b int, near bool) {
	m := l.scores.len()
	k := bestSplit(l.scores.column(l.h), l.scores.column(c))
	cut := binsPerStep*(b-m+1+k) + 35 // the first decision that belongs to the new alignment
	cut = max(cut, l.base)
	kept := l.queue[:0]
	for _, it := range l.queue { // characters still held back from after the change are dropped
		if it.end <= cut {
			kept = append(kept, it)
		}
	}
	l.queue = kept
	l.h = c
	l.neighbours[0], l.neighbours[1] = slipNeighbours(c)
	if !near {
		l.comb = newCombiner() // an unrelated alignment: the letters/figures state cannot be trusted
	}
	l.alignGroups(c, cut)
	l.recent = l.recent[:0]
	l.resetRun()
	l.resetGhost()
	l.lowRun, l.ownLowRun, l.challenge, l.dwell = 0, 0, 0, 0
	l.switches++
	l.advanceGroups() // read the groups already received under the new alignment
}
