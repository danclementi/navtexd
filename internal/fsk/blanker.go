// Package fsk holds the FSK demodulation stages: impulse blanker, narrow channel filter, discriminator,
// signal-presence detector, symbol timing and integrate-and-dump. Everything is causal and streaming.
package fsk

import (
	"math/cmplx"
	"sort"
)

// Blanker zeroes samples whose magnitude exceeds K times the median magnitude of their 0.5 s block, plus one
// neighbour each side (as blank_impulses in the reference). The output is delayed by up to one block (the median
// needs the whole block); call Flush at end of stream to release the rest.
type Blanker struct {
	k        float64
	blockLen int
	x        []complex128 // samples not yet emitted
	over     []bool       // over[i] for x[i], known for i < len(over)
	prevOver bool         // over flag of the last emitted sample
	out      []complex128
	Blanked  int64 // samples zeroed so far
	Total    int64 // samples emitted so far
}

// NewBlanker creates a blanker for sample rate fs. k <= 0 disables blanking (pass-through, no delay).
func NewBlanker(fs, k float64) *Blanker {
	b := int(0.5 * fs)
	if b < 8 {
		b = 8
	}
	return &Blanker{k: k, blockLen: b}
}

// SetK changes the threshold while running. Switching blanking on or off changes the delay and is not done here
// (it reports false and leaves the threshold alone).
func (b *Blanker) SetK(k float64) bool {
	if (k > 0) != (b.k > 0) {
		return false
	}
	b.k = k
	return true
}

func (b *Blanker) markBlock(blk []complex128) {
	mag := make([]float64, len(blk))
	for i, v := range blk {
		mag[i] = cmplx.Abs(v)
	}
	sorted := append([]float64(nil), mag...)
	sort.Float64s(sorted)
	var med float64
	if n := len(sorted); n%2 == 1 {
		med = sorted[n/2]
	} else {
		med = 0.5 * (sorted[n/2-1] + sorted[n/2])
	}
	thr := med * b.k
	for _, m := range mag {
		b.over = append(b.over, m > thr)
	}
}

func (b *Blanker) emit(n int, last bool) {
	for i := 0; i < n; i++ {
		next := false
		if i+1 < len(b.over) {
			next = b.over[i+1]
		} else if !last {
			panic("blanker: lookahead missing")
		}
		v := b.x[i]
		if b.prevOver || b.over[i] || next {
			v = 0
			b.Blanked++
		}
		b.prevOver = b.over[i]
		b.out = append(b.out, v)
	}
	b.Total += int64(n)
	b.x = append(b.x[:0], b.x[n:]...)
	b.over = append(b.over[:0], b.over[n:]...)
}

// Process consumes in and returns the samples that are ready. The result is valid until the next call.
func (b *Blanker) Process(in []complex128) []complex128 {
	b.out = b.out[:0]
	if b.k <= 0 {
		b.Total += int64(len(in))
		return append(b.out, in...)
	}
	b.x = append(b.x, in...)
	for len(b.x)-len(b.over) >= b.blockLen {
		b.markBlock(b.x[len(b.over) : len(b.over)+b.blockLen])
	}
	if n := len(b.over) - 1; n > 0 {
		b.emit(n, false)
	}
	return b.out
}

// Flush processes the final partial block and returns everything that is still held back.
func (b *Blanker) Flush() []complex128 {
	b.out = b.out[:0]
	if b.k <= 0 {
		return b.out
	}
	if rest := b.x[len(b.over):]; len(rest) > 0 {
		b.markBlock(rest)
	}
	b.emit(len(b.x), true)
	return b.out
}

// BlankedFraction returns the fraction of emitted samples that were zeroed.
func (b *Blanker) BlankedFraction() float64 {
	if b.Total == 0 {
		return 0
	}
	return float64(b.Blanked) / float64(b.Total)
}
