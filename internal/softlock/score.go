package softlock

import "math"

// Scoring: how well does each possible alignment of the bit stream look like SITOR-B? Every character is sent twice,
// five slots (35 bits) apart. With the right alignment a group of 7 bits and the group 35 bits later are two copies of
// one character, so their soft values agree; with the wrong alignment they are unrelated. A score measures that
// agreement and adding scores over a window of comparisons makes the right alignment stand out in heavy noise.
//
// There are 14 hypotheses per polarity: 7 bit phases times 2 parities; hypothesis h of a block is phase h/2 and parity
// h%2.
//
// Derived from navtex-core/src/score.rs of navtex-rs (bgm60), GPL-3.0-or-later.

const (
	perPolarity = 14 // alignment hypotheses per polarity
	binsPerStep = 14 // bit decisions per comparison: two 7-bit slots
	splitOffset = 35 // distance between the two copies of a character, in bit decisions
	lastNeeded  = 55 // decisions needed beyond 14*b before comparison b can be scored
	eps         = 1e-12
)

// binsAvailable is how many comparisons can be scored from n bit decisions.
func binsAvailable(n int) int {
	if n < lastNeeded {
		return 0
	}
	return (n-lastNeeded)/binsPerStep + 1
}

// comparisonScore scores one pair of groups (x, and y five slots later); high means "two copies of one character".
// It adds two measures, each shifted and scaled by its typical value on pure noise: the cosine of the angle between x
// and y, and the best match of x+y against any of the 32 traffic codes.
func comparisonScore(x, y *[7]float64, p *Params) float64 {
	var xy, xx, yy float64
	var both [7]float64
	for k := 0; k < 7; k++ {
		xy += x[k] * y[k]
		xx += x[k] * x[k]
		yy += y[k] * y[k]
		both[k] = x[k] + y[k]
	}
	cosine := xy / (math.Sqrt(xx*yy) + eps)
	var bothEnergy float64
	for _, v := range both {
		bothEnergy += v * v
	}
	best := math.Inf(-1)
	for _, code := range trafficCodes {
		var agreement float64
		for k := 0; k < 7; k++ {
			if code&(1<<(6-k)) != 0 {
				agreement += both[k]
			} else {
				agreement -= both[k]
			}
		}
		if agreement > best {
			best = agreement
		}
	}
	correlation := best / (math.Sqrt(7)*math.Sqrt(bothEnergy) + eps)
	return (cosine-p.CosineNull[0])/p.CosineNull[1] + (correlation-p.CorrelationNull[0])/p.CorrelationNull[1]
}

// group copies seven consecutive soft values starting at decision number start (history[0] is decision number base),
// multiplied by sign.
func group(history []float64, base, start int, sign float64) [7]float64 {
	var g [7]float64
	for k := range g {
		g[k] = sign * history[start+k-base]
	}
	return g
}

// binScores gives the scores of comparison b for every hypothesis (perPolarity per sign in signs).
func binScores(history []float64, base, b int, signs []float64, p *Params, out []float64) {
	for blk, sign := range signs {
		for h := 0; h < perPolarity; h++ {
			phase, parity := h/2, h%2
			// parity 0 takes the odd group of the pair as the earlier copy, parity 1 the even one
			first := 2 * b
			if parity == 0 {
				first++
			}
			xs := phase + 7*first
			x := group(history, base, xs, sign)
			y := group(history, base, xs+splitOffset, sign)
			out[blk*perPolarity+h] = comparisonScore(&x, &y, p)
		}
	}
}

// scoreWindow keeps the most recent comparison scores so they can be added up into z-scores.
type scoreWindow struct {
	capacity int
	width    int
	rows     [][]float64
}

func newScoreWindow(capacity, width int) *scoreWindow {
	return &scoreWindow{capacity: capacity, width: width}
}

func (w *scoreWindow) push(row []float64) {
	r := make([]float64, w.width)
	copy(r, row)
	if len(w.rows) == w.capacity {
		copy(w.rows, w.rows[1:])
		w.rows[len(w.rows)-1] = r
		return
	}
	w.rows = append(w.rows, r)
}

func (w *scoreWindow) clear()     { w.rows = w.rows[:0] }
func (w *scoreWindow) len() int   { return len(w.rows) }
func (w *scoreWindow) full() bool { return len(w.rows) == w.capacity }
func (w *scoreWindow) column(h int) []float64 {
	c := make([]float64, len(w.rows))
	for i, r := range w.rows {
		c[i] = r[h]
	}
	return c
}

// sumsLast adds the newest n rows (all of them if there are fewer).
func (w *scoreWindow) sumsLast(n int, out []float64) {
	for h := range out {
		out[h] = 0
	}
	from := len(w.rows) - n
	if from < 0 {
		from = 0
	}
	for _, r := range w.rows[from:] {
		for h := range out {
			out[h] += r[h]
		}
	}
}

// zLast gives z-scores over the newest n rows; the divisor always assumes n rows.
func (w *scoreWindow) zLast(n int, p *Params, out []float64) {
	w.sumsLast(n, out)
	scale := p.ScoreSD * math.Sqrt(float64(n))
	for h := range out {
		out[h] /= scale
	}
}

// z gives the window sums scaled to z-scores: a sum of Window independent noise scores has standard deviation
// ScoreSD*sqrt(Window).
func (w *scoreWindow) z(p *Params, out []float64) {
	w.sumsLast(len(w.rows), out)
	scale := p.ScoreSD * math.Sqrt(float64(p.Window))
	for h := range out {
		out[h] /= scale
	}
}
