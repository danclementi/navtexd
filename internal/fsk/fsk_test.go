package fsk

import (
	"math"
	"reflect"
	"testing"

	"navtex/internal/ddc"
	"navtex/internal/iq"
	"navtex/internal/testvec"
)

type chMeta struct {
	Channels map[string]struct {
		ShiftHz        float64 `json:"shift_hz"`
		Decim          int     `json:"decimation"`
		Fs2            float64 `json:"fs2"`
		BlankedFrac    float64 `json:"blanked_fraction"`
		F0Refined      float64 `json:"f0_refined_hz"`
		NSymbols       int     `json:"n_symbols"`
		NLockedSymbols int     `json:"n_locked_symbols"`
	} `json:"channels"`
	CentreHz float64 `json:"centre_hz"`
}

var cases = []string{"nominal", "usb_real"}

func relRMS(got []complex128, want []complex64) float64 {
	var num, den float64
	for i := range want {
		w := complex128(want[i])
		d := got[i] - w
		num += real(d)*real(d) + imag(d)*imag(d)
		den += real(w)*real(w) + imag(w)*imag(w)
	}
	return math.Sqrt(num / den)
}

func to128(x []complex64) []complex128 {
	out := make([]complex128, len(x))
	for i, v := range x {
		out[i] = complex128(v)
	}
	return out
}

func feedBlanker(b *Blanker, x []complex128, block int) []complex128 {
	var out []complex128
	for i := 0; i < len(x); i += block {
		j := min(i+block, len(x))
		out = append(out, b.Process(x[i:j])...)
	}
	return append(out, b.Flush()...)
}

func feedDemod(d *Demod, x []complex128, block int) []Symbol {
	var out []Symbol
	for i := 0; i < len(x); i += block {
		j := min(i+block, len(x))
		out = append(out, d.Process(x[i:j])...)
	}
	return append(out, d.Flush()...)
}

// ddc output of the Go DDC for a case, from its WAV.
func goDDC(t *testing.T, name string, m chMeta) []complex128 {
	w, err := iq.OpenWav(testvec.Path(t, name+".wav"), iq.WavOptions{CentreHz: m.CentreHz, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ch := m.Channels["518000"]
	d := ddc.New(w.SampleRate(), ch.ShiftHz, ch.Decim)
	var out []complex128
	buf := make([]complex64, 8192)
	for {
		n, err := w.Read(buf)
		out = append(out, d.Process(buf[:n])...)
		if err != nil {
			return out
		}
	}
}

func TestBlankerGolden(t *testing.T) {
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			var m chMeta
			testvec.JSON(t, name+".json", &m)
			ch := m.Channels["518000"]
			in := goDDC(t, name, m)
			want := testvec.C64(t, name+"_518_blanked.c64")
			for _, block := range []int{1, 777, 100000} {
				b := NewBlanker(ch.Fs2, 6.0)
				got := feedBlanker(b, in, block)
				if len(got) != len(want) {
					t.Fatalf("block %d: %d samples, want %d", block, len(got), len(want))
				}
				if e := relRMS(got, want); e > 1e-6 {
					t.Errorf("block %d: rel RMS %g", block, e)
				}
				if math.Abs(b.BlankedFraction()-ch.BlankedFrac) > 1e-4 {
					t.Errorf("blanked fraction %g, reference %g", b.BlankedFraction(), ch.BlankedFrac)
				}
			}
		})
	}
}

// Feed the reference's own blanked stream so only the demodulator stages are under test.
func TestDemodGolden(t *testing.T) {
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			var m chMeta
			testvec.JSON(t, name+".json", &m)
			ch := m.Channels["518000"]
			xb := to128(testvec.C64(t, name+"_518_blanked.c64"))
			wantSoft := testvec.F32(t, name+"_518_soft.f32")
			wantLock := testvec.Bytes(t, name+"_518_lock.u8")
			wantNarrow := testvec.C64(t, name+"_518_narrow.c64")

			d := NewDemod(DefaultParams(ch.Fs2), ch.F0Refined)
			d.KeepNarrowOutput = true
			syms := feedDemod(d, xb, 4096)
			if e := relRMS(d.Narrow, wantNarrow); e > 1e-6 {
				t.Errorf("narrow output rel RMS %g", e)
			}
			if len(syms) != len(wantSoft) {
				t.Fatalf("%d symbols, reference %d", len(syms), len(wantSoft))
			}
			var maxErr float64
			mism := 0
			for i, s := range syms {
				if s.Locked != (wantLock[i] != 0) {
					mism++
					continue
				}
				maxErr = math.Max(maxErr, math.Abs(s.Soft-float64(wantSoft[i])))
			}
			if mism != 0 {
				t.Errorf("%d lock mismatches out of %d", mism, len(syms))
			}
			if maxErr > 1e-3 { // Hz; soft values are ~85 Hz
				t.Errorf("max soft error %g Hz", maxErr)
			}
			t.Logf("%d symbols, %d locked, max soft error %.2g Hz", len(syms), ch.NLockedSymbols, maxErr)
		})
	}
}

func TestDemodBlockInvariance(t *testing.T) {
	var m chMeta
	testvec.JSON(t, "usb_real.json", &m)
	ch := m.Channels["518000"]
	xb := to128(testvec.C64(t, "usb_real_518_blanked.c64"))
	ref := feedDemod(NewDemod(DefaultParams(ch.Fs2), ch.F0Refined), xb, 65536)
	for _, b := range []int{1, 13, 4096, 100003} {
		got := feedDemod(NewDemod(DefaultParams(ch.Fs2), ch.F0Refined), xb, b)
		if !reflect.DeepEqual(got, ref) {
			t.Fatalf("block size %d differs", b)
		}
	}
}

// Whole chain from the WAV, with the reference's final carrier offset.
func TestEndToEndSoft(t *testing.T) {
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			var m chMeta
			testvec.JSON(t, name+".json", &m)
			ch := m.Channels["518000"]
			b := NewBlanker(ch.Fs2, 6.0)
			xb := feedBlanker(b, goDDC(t, name, m), 5000)
			syms := feedDemod(NewDemod(DefaultParams(ch.Fs2), ch.F0Refined), xb, 5000)
			wantSoft := testvec.F32(t, name+"_518_soft.f32")
			wantLock := testvec.Bytes(t, name+"_518_lock.u8")
			if len(syms) != len(wantSoft) {
				t.Fatalf("%d symbols, want %d", len(syms), len(wantSoft))
			}
			mism := 0
			var maxErr float64
			for i, s := range syms {
				if s.Locked != (wantLock[i] != 0) {
					mism++
				} else {
					maxErr = math.Max(maxErr, math.Abs(s.Soft-float64(wantSoft[i])))
				}
			}
			if mism != 0 || maxErr > 1e-2 {
				t.Errorf("lock mismatches %d, max soft error %g Hz", mism, maxErr)
			}
		})
	}
}
