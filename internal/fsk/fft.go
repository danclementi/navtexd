package fsk

import (
	"math"
	"math/bits"
)

// fft computes the in-place radix-2 forward DFT of x; len(x) must be a power of two.
func fft(x []complex128) {
	n := len(x)
	shift := 64 - uint(bits.TrailingZeros(uint(n)))
	for i := 0; i < n; i++ {
		j := int(bits.Reverse64(uint64(i)) >> shift)
		if j > i {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size / 2
		w := complex(math.Cos(2*math.Pi/float64(size)), -math.Sin(2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			t := complex(1, 0)
			for k := 0; k < half; k++ {
				a, b := x[start+k], x[start+k+half]*t
				x[start+k], x[start+k+half] = a+b, a-b
				t *= w
			}
		}
	}
}

// FFT computes the in-place radix-2 forward DFT of x (len(x) must be a power of two).
func FFT(x []complex128) { fft(x) }
