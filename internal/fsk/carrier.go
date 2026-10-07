package fsk

import (
	"math"
	"sort"
)

// CarrierSearch estimates the carrier offset of a two-tone FSK signal from a window of complex baseband: Welch PSD,
// 20 Hz smoothing, then a comb S(f0-shift/2)+S(f0+shift/2) scanned over +/-Search Hz (the reference's estimate_carrier).
type CarrierSearch struct {
	Fs, Shift, Search float64
	NFFT              int
	win               []float64
}

func NewCarrierSearch(fs, shift, search float64) *CarrierSearch {
	n := 2048
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)) // periodic Hann
	}
	return &CarrierSearch{Fs: fs, Shift: shift, Search: search, NFFT: n, win: w}
}

// Estimate returns the best offset (Hz) and a contrast figure: comb peak divided by the median comb score.
// x must hold at least NFFT samples.
func (c *CarrierSearch) Estimate(x []complex128) (f0, contrast float64) {
	f0, contrast, _, _ = c.EstimateRef(x, 0)
	return f0, contrast
}

// EstimateRef is Estimate that also returns the comb score at the reference offset ref and the best score, so a
// caller can tell whether the best position is clearly better than the one it is using.
func (c *CarrierSearch) EstimateRef(x []complex128, ref float64) (f0, contrast, refScore, bestScore float64) {
	n := c.NFFT
	psd := make([]float64, n)
	buf := make([]complex128, n)
	segs := 0
	for s := 0; s+n <= len(x); s += n / 2 {
		for i := 0; i < n; i++ {
			buf[i] = x[s+i] * complex(c.win[i], 0)
		}
		fft(buf)
		for i, v := range buf {
			psd[i] += real(v)*real(v) + imag(v)*imag(v)
		}
		segs++
	}
	if segs == 0 {
		return 0, 0, 0, 0
	}
	// order bins from -fs/2 upwards: index k -> frequency (k-n/2)*df
	df := c.Fs / float64(n)
	P := make([]float64, n)
	for k := 0; k < n; k++ {
		P[k] = psd[(k+n/2)%n]
	}
	k := int(math.Round(20 / df))
	if k < 1 {
		k = 1
	}
	Ps := make([]float64, n) // moving average, zero padded ("same")
	var acc float64
	for i := 0; i < n+k; i++ {
		if i < n {
			acc += P[i]
		}
		if o := i - k; o >= 0 {
			acc -= P[o]
		}
		if c0 := i - k/2; c0 >= 0 && c0 < n {
			Ps[c0] = acc / float64(k)
		}
	}
	at := func(f float64) float64 {
		p := f/df + float64(n/2)
		if p <= 0 {
			return Ps[0]
		}
		if p >= float64(n-1) {
			return Ps[n-1]
		}
		i := int(p)
		return Ps[i] + (p-float64(i))*(Ps[i+1]-Ps[i])
	}
	var scores []float64
	best, bestF := math.Inf(-1), 0.0
	for g := -c.Search; g <= c.Search+1e-9; g += 0.5 {
		s := at(g-c.Shift/2) + at(g+c.Shift/2)
		scores = append(scores, s)
		if s > best {
			best, bestF = s, g
		}
	}
	sort.Float64s(scores)
	med := scores[len(scores)/2]
	return bestF, best / (med + 1e-30), at(ref-c.Shift/2) + at(ref+c.Shift/2), best
}
