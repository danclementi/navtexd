// Package iq defines the sample Source interface and file/stream implementations.
package iq

// Kind says how the samples returned by a Source should be interpreted.
type Kind int

const (
	// IQ: samples are complex baseband (I in real, Q in imag).
	IQ Kind = iota
	// Real: samples are real-valued audio (imag == 0). CentreHz is then the
	// receiver LO frequency (the RF frequency that maps to 0 Hz audio).
	Real
)

// Source delivers samples in blocks. Time is the sample index, never wall-clock.
type Source interface {
	SampleRate() float64
	CentreHz() float64
	Kind() Kind
	// Read fills dst with up to len(dst) samples. It returns io.EOF (with n == 0)
	// at end of stream. Live sources block until data is available.
	Read(dst []complex64) (int, error)
}
