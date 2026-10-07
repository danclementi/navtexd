package ddc

// Decimator is a streaming FIR low-pass plus decimate-by-Q. State (input history and the decimation phase)
// persists between calls, so any block partition of the input gives bit-identical output.
type Decimator struct {
	q    int
	h    []float64
	hist []complex128 // last len(h)-1 input samples
	skip int          // input samples to skip before the next output
	buf  []complex128 // scratch: hist followed by the current block
	out  []complex128
}

// NewDecimator returns a decimator with the given taps.
func NewDecimator(q int, taps []float64) *Decimator {
	return &Decimator{q: q, h: append([]float64(nil), taps...), hist: make([]complex128, len(taps)-1)}
}

// Factor returns the decimation factor.
func (d *Decimator) Factor() int { return d.q }

// Process filters and decimates in. The returned slice is only valid until the next call.
func (d *Decimator) Process(in []complex128) []complex128 {
	nh := len(d.hist)
	need := nh + len(in)
	if cap(d.buf) < need {
		d.buf = make([]complex128, need)
	}
	buf := d.buf[:need]
	copy(buf, d.hist)
	copy(buf[nh:], in)

	d.out = d.out[:0]
	i := d.skip
	for ; i < len(in); i += d.q {
		// y[i] = sum_k h[k] * x[i-k]; x[i] is buf[nh+i]
		var sr, si float64
		base := nh + i
		for k, hk := range d.h {
			v := buf[base-k]
			sr += hk * real(v)
			si += hk * imag(v)
		}
		d.out = append(d.out, complex(sr, si))
	}
	d.skip = i - len(in)
	copy(d.hist, buf[need-nh:])
	return d.out
}
