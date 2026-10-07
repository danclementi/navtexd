// Package ddc implements the streaming digital down-converter: a phase-continuous NCO mixer followed by a
// cascade of FIR decimators. It follows the design of the original Python prototype (not distributed with this program).
package ddc

import "math"

// PickDecim returns the integer decimation near fs/target whose prime factors are all <= 13.
func PickDecim(fs, target float64) int {
	d0 := fs / target
	lo := int(d0 * 0.6)
	if lo < 1 {
		lo = 1
	}
	best := 0
	for d := lo; d < int(d0*1.5)+2; d++ {
		n := d
		for _, p := range []int{2, 3, 5, 7, 11, 13} {
			for n%p == 0 {
				n /= p
			}
		}
		if n == 1 && (best == 0 || math.Abs(float64(d)-d0) < math.Abs(float64(best)-d0)) {
			best = d
		}
	}
	return best
}

// StageFactors splits q into cascade stage factors, each <= 10 where the prime factors allow.
func StageFactors(q int) []int {
	var primes, stages []int
	n := q
	for _, p := range []int{13, 11, 7, 5, 3, 2} {
		for n%p == 0 {
			primes = append(primes, p)
			n /= p
		}
	}
	cur := 1
	for _, p := range primes {
		if cur == 1 || cur*p <= 10 {
			cur *= p
		} else {
			stages = append(stages, cur)
			cur = p
		}
	}
	if cur > 1 {
		stages = append(stages, cur)
	}
	return stages
}

// bessel0 is the modified Bessel function of the first kind, order 0 (power series).
func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 200; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < 1e-18*sum {
			break
		}
	}
	return sum
}

// kaiserOrd mirrors scipy.signal.kaiserord for ripple > 50 dB: tap count and beta for a transition
// width given as a fraction of the Nyquist frequency.
func kaiserOrd(ripple, width float64) (int, float64) {
	beta := 0.1102 * (ripple - 8.7)
	n := (ripple-7.95)/2.285/(math.Pi*width) + 1
	return int(math.Ceil(n)), beta
}

// firwinLowpass mirrors scipy.signal.firwin(n, cutoff, window=("kaiser", beta), fs=fs) with unity DC gain.
func firwinLowpass(n int, cutoffHz, beta, fs float64) []float64 {
	c := cutoffHz / (fs / 2)
	alpha := 0.5 * float64(n-1)
	h := make([]float64, n)
	i0b := bessel0(beta)
	var sum float64
	for i := range h {
		m := float64(i) - alpha
		sinc := 1.0
		if m != 0 {
			sinc = math.Sin(math.Pi*c*m) / (math.Pi * c * m)
		}
		r := 0.0
		if n > 1 {
			r = 2*float64(i)/float64(n-1) - 1
		}
		w := bessel0(beta*math.Sqrt(math.Max(0, 1-r*r))) / i0b
		h[i] = c * sinc * w
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}

// DesignDecimator designs the reference's decimate-by-q anti-alias filter for input rate fsIn:
// Kaiser 60 dB, passband 0.4*fsOut, stopband 0.6*fsOut, odd length, unity DC gain.
func DesignDecimator(q int, fsIn float64) []float64 {
	fsOut := fsIn / float64(q)
	n, beta := kaiserOrd(60, 0.2*fsOut/(fsIn/2))
	n |= 1
	return firwinLowpass(n, 0.5*fsOut, beta, fsIn)
}

// DesignLowpass designs a Kaiser low-pass (odd length, unity DC gain) like the reference narrow_filter:
// cutoffHz is the -6 dB point, transitionHz the transition width, atten the stop-band attenuation in dB.
func DesignLowpass(fs, cutoffHz, transitionHz, atten float64) []float64 {
	n, beta := kaiserOrd(atten, transitionHz/(fs/2))
	n |= 1
	return firwinLowpass(n, cutoffHz, beta, fs)
}
