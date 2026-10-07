package engine

import (
	"math"

	"navtex/internal/config"
	"navtex/internal/fsk"
	"navtex/internal/iq"
)

// specState computes a smoothed and a raw spectrum of the input at the meter rate.
type specState struct {
	n, hop    int
	win       []float64
	winPow    float64
	ring      []complex64
	pos, fill int
	since     int
	k0, bins  int
	step      float64
	alpha     float64
	smooth    []float64
	buf       []complex128
	pending   *Spectrum
	// fullScaleDB is the level, in the units of the spectrum, of a tone of full scale (amplitude 1)
	fullScaleDB float64
	iq          bool
}

func newSpec(fs float64, kind iq.Kind, shift float64, m config.Meters) *specState {
	n := m.SpectrumFFT
	if n == 0 {
		n = 256
		for float64(n) < fs/6 {
			n <<= 1
		}
	}
	s := &specState{n: n, hop: int(fs / m.RateHz), step: fs / float64(n), alpha: m.SpectrumSmooth}
	if s.hop < 1 {
		s.hop = 1
	}
	s.win = make([]float64, n)
	for i := range s.win {
		s.win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
		s.winPow += s.win[i] * s.win[i]
	}
	sumW := 0.0
	for _, w := range s.win {
		sumW += w
	}
	p := sumW * sumW / s.winPow // complex tone of magnitude 1
	if kind == iq.Real {
		p /= 4 // a real tone of amplitude 1 splits into two half-amplitude components
	}
	s.fullScaleDB = 10 * math.Log10(p)
	s.ring = make([]complex64, n)
	s.buf = make([]complex128, n)
	maxHz := math.Min(m.SpectrumMaxHz, fs/2)
	if kind == iq.Real {
		s.k0, s.bins = 0, int(maxHz/s.step)+1
	} else {
		half := math.Min(maxHz/2, fs/2-1)
		s.k0 = int(math.Round((shift - half) / s.step))
		s.bins = int(2*half/s.step) + 1
	}
	s.smooth = make([]float64, s.bins)
	return s
}

func (s *specState) feed(block []complex64) {
	for _, x := range block {
		s.ring[s.pos] = x
		s.pos++
		if s.pos == s.n {
			s.pos = 0
		}
		if s.fill < s.n {
			s.fill++
		}
		s.since++
		if s.since >= s.hop && s.fill == s.n {
			s.since = 0
			s.compute()
		}
	}
}

func (s *specState) compute() {
	for i := 0; i < s.n; i++ {
		v := s.ring[(s.pos+i)%s.n]
		s.buf[i] = complex(float64(real(v))*s.win[i], float64(imag(v))*s.win[i])
	}
	fsk.FFT(s.buf)
	sp := &Spectrum{StartHz: float64(s.k0) * s.step, StepHz: s.step, Filtered: make([]float32, s.bins), Raw: make([]float32, s.bins)}
	first := s.smooth[0] == 0
	for j := 0; j < s.bins; j++ {
		k := ((s.k0+j)%s.n + s.n) % s.n
		c := s.buf[k]
		p := (real(c)*real(c) + imag(c)*imag(c)) / s.winPow
		if first {
			s.smooth[j] = p
		} else {
			s.smooth[j] = s.alpha*s.smooth[j] + (1-s.alpha)*p
		}
		sp.Raw[j] = float32(10 * math.Log10(p+1e-15))
		sp.Filtered[j] = float32(10 * math.Log10(s.smooth[j]+1e-15))
	}
	s.pending = sp
}

// take returns the newest frame not yet reported, or nil.
func (s *specState) take() *Spectrum {
	sp := s.pending
	s.pending = nil
	return sp
}
