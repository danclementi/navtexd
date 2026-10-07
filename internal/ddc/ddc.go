package ddc

import "math"

// Mixer multiplies by exp(-j*2*pi*shift*n/fs) using a 64-bit phase accumulator, so the phase is continuous
// across blocks, exact for any block size, and free of drift over long runs.
type Mixer struct {
	inc, acc uint64
}

// NewMixer shifts a component at +shiftHz down to 0 Hz (shiftHz may be negative).
func NewMixer(fs, shiftHz float64) *Mixer {
	frac := shiftHz / fs
	frac -= math.Floor(frac) // into [0,1)
	return &Mixer{inc: uint64(frac * (1 << 64))}
}

const twoPiOver2p64 = 2 * math.Pi / (1 << 64)

// Process mixes in into out (which must have len >= len(in)); in and out may not overlap partially.
func (m *Mixer) Process(in []complex64, out []complex128) {
	for i, v := range in {
		s, c := math.Sincos(float64(m.acc) * twoPiOver2p64)
		// (a+jb) * (c - js)
		a, b := float64(real(v)), float64(imag(v))
		out[i] = complex(a*c+b*s, b*c-a*s)
		m.acc += m.inc
	}
}

// Process128 is Process for complex128 input. in and out may be the same slice.
func (m *Mixer) Process128(in, out []complex128) {
	for i, v := range in {
		s, c := math.Sincos(float64(m.acc) * twoPiOver2p64)
		a, b := real(v), imag(v)
		out[i] = complex(a*c+b*s, b*c-a*s)
		m.acc += m.inc
	}
}

// SetShift changes the mixing frequency; the phase stays continuous.
func (m *Mixer) SetShift(fs, shiftHz float64) { m.inc = NewMixer(fs, shiftHz).inc }

// DDC is a Mixer followed by a cascade of Decimators.
type DDC struct {
	mix    *Mixer
	stages []*Decimator
	tmp    []complex128
	fsOut  float64
}

// New builds a DDC with reference-style Kaiser filters: decimation totalQ split by StageFactors.
func New(fs, shiftHz float64, totalQ int) *DDC {
	var stages []*Decimator
	f := fs
	for _, q := range StageFactors(totalQ) {
		stages = append(stages, NewDecimator(q, DesignDecimator(q, f)))
		f /= float64(q)
	}
	return NewWithStages(fs, shiftHz, stages)
}

// NewWithStages builds a DDC from explicit stages (e.g. the reference's taps from filters.json).
func NewWithStages(fs, shiftHz float64, stages []*Decimator) *DDC {
	f := fs
	for _, s := range stages {
		f /= float64(s.q)
	}
	return &DDC{mix: NewMixer(fs, shiftHz), stages: stages, fsOut: f}
}

// OutputRate is the sample rate after the last stage.
func (d *DDC) OutputRate() float64 { return d.fsOut }

// Process mixes and decimates one block. The result is valid until the next call.
func (d *DDC) Process(in []complex64) []complex128 {
	if cap(d.tmp) < len(in) {
		d.tmp = make([]complex128, len(in))
	}
	y := d.tmp[:len(in)]
	d.mix.Process(in, y)
	for _, s := range d.stages {
		y = s.Process(y)
	}
	return y
}
