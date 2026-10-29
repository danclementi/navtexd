package softlock

import "math"

// NullReport describes how the comparison scores behave on a soft-value stream that carries no NAVTEX signal.
type NullReport struct {
	Comparisons int        // comparisons scored (each gives one score per hypothesis)
	Cosine      [2]float64 // mean, sd of the cosine term
	Correlation [2]float64 // mean, sd of the correlation term
	ScoreSD     float64    // sd of one comparison score with the null parameters of p
	ScoreMean   float64
	Lag1        float64 // correlation of a hypothesis's score with its score one comparison later (independence check)
	ZSD         float64 // sd of the window z-score (sum over p.Window comparisons / (ScoreSD*sqrt(Window)))
	ZMax        float64 // largest window z-score seen on any hypothesis
	FracOver    float64 // fraction of window z-scores at or above p.ZStar
	Locks       int     // how many times a lock would have been declared on this stream (lock rule only)
}

// MeasureNull runs the scoring over soft values that contain no signal and reports the statistics that the lock's
// thresholds assume (see docs/soft-lock-design.md, section 6). With the right parameters ZSD is close to 1; ScoreSD and
// the two null pairs are what to put into Params.
func MeasureNull(soft []float64, p Params) NullReport {
	var rep NullReport
	n := binsAvailable(len(soft))
	rep.Comparisons = n
	if n < p.Window+2 {
		return rep
	}
	signs := []float64{1}
	var cos, cor, sc [perPolarity][]float64
	for b := 0; b < n; b++ {
		for h := 0; h < perPolarity; h++ {
			phase, parity := h/2, h%2
			first := 2 * b
			if parity == 0 {
				first++
			}
			xs := phase + 7*first
			x := group(soft, 0, xs, signs[0])
			y := group(soft, 0, xs+splitOffset, signs[0])
			c1, c2 := components(&x, &y)
			cos[h] = append(cos[h], c1)
			cor[h] = append(cor[h], c2)
			sc[h] = append(sc[h], (c1-p.CosineNull[0])/p.CosineNull[1]+(c2-p.CorrelationNull[0])/p.CorrelationNull[1])
		}
	}
	var allCos, allCor, allSc []float64
	for h := 0; h < perPolarity; h++ {
		allCos = append(allCos, cos[h]...)
		allCor = append(allCor, cor[h]...)
		allSc = append(allSc, sc[h]...)
	}
	rep.Cosine[0], rep.Cosine[1] = meanSD(allCos)
	rep.Correlation[0], rep.Correlation[1] = meanSD(allCor)
	rep.ScoreMean, rep.ScoreSD = meanSD(allSc)
	var num, den float64
	for h := 0; h < perPolarity; h++ {
		m, _ := meanSD(sc[h])
		for i := 0; i+1 < len(sc[h]); i++ {
			num += (sc[h][i] - m) * (sc[h][i+1] - m)
		}
		for _, v := range sc[h] {
			den += (v - m) * (v - m)
		}
	}
	if den > 0 {
		rep.Lag1 = num / den
	}
	// window z-scores, as the lock computes them (with the SD and window in p), and the lock rule on the 14 hypotheses
	scale := p.ScoreSD * math.Sqrt(float64(p.Window))
	var zs []float64
	var run [perPolarity]float64
	prevLocked := false
	for b := 0; b < n; b++ {
		for h := 0; h < perPolarity; h++ {
			run[h] += sc[h][b]
			if b >= p.Window {
				run[h] -= sc[h][b-p.Window]
			}
		}
		if b+1 < p.Window {
			continue
		}
		top, second := 0, math.Inf(-1)
		var z [perPolarity]float64
		for h := range z {
			z[h] = run[h] / scale
			zs = append(zs, z[h])
			if z[h] > z[top] {
				top = h
			}
			if z[h] >= p.ZStar {
				rep.FracOver++
			}
			rep.ZMax = math.Max(rep.ZMax, z[h])
		}
		for h := range z {
			if h != top && z[h] > second {
				second = z[h]
			}
		}
		now := z[top] >= p.ZStar && z[top]-second >= p.Gap
		if now && !prevLocked {
			rep.Locks++
		}
		prevLocked = now
	}
	_, rep.ZSD = meanSD(zs)
	rep.FracOver /= float64(len(zs))
	return rep
}

func meanSD(v []float64) (mean, sd float64) {
	if len(v) == 0 {
		return 0, 0
	}
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	for _, x := range v {
		sd += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sd / float64(len(v)))
}
