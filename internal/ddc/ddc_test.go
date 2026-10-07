package ddc

import (
	"math"
	"math/cmplx"
	"reflect"
	"testing"

	"navtex/internal/iq"
	"navtex/internal/testvec"
)

// Expected values were produced by the Python reference's pick_decim/stage_factors.
func TestPickAndFactors(t *testing.T) {
	for _, c := range []struct {
		fs     float64
		d      int
		stages []int
	}{{64000, 16, []int{8, 2}}, {8000, 2, []int{2}}, {62500, 16, []int{8, 2}}, {48000, 12, []int{6, 2}}, {4000, 1, nil}, {96000, 24, []int{6, 4}}, {192000, 48, []int{6, 8}}, {44100, 11, []int{11}}, {11025, 3, []int{3}}} {
		d := PickDecim(c.fs, 4000)
		if d != c.d {
			t.Errorf("PickDecim(%v) = %d, want %d", c.fs, d, c.d)
		}
		if got := StageFactors(d); !reflect.DeepEqual(got, c.stages) {
			t.Errorf("StageFactors(%d) = %v, want %v", d, got, c.stages)
		}
	}
}

type filtersFile struct {
	Stages []struct {
		Q    int       `json:"q"`
		FsIn float64   `json:"fs_in"`
		Taps []float64 `json:"taps"`
	} `json:"stages"`
}

// The Go Kaiser design must reproduce scipy's taps.
func TestDesignMatchesScipy(t *testing.T) {
	for _, name := range []string{"filters.json", "usb_real_filters.json"} {
		var f filtersFile
		testvec.JSON(t, name, &f)
		for _, s := range f.Stages {
			h := DesignDecimator(s.Q, s.FsIn)
			if len(h) != len(s.Taps) {
				t.Fatalf("%s q=%d: %d taps, want %d", name, s.Q, len(h), len(s.Taps))
			}
			var maxd float64
			for i := range h {
				maxd = math.Max(maxd, math.Abs(h[i]-s.Taps[i]))
			}
			if maxd > 1e-12 {
				t.Errorf("%s q=%d: max tap diff %g", name, s.Q, maxd)
			}
		}
	}
}

func drain(t *testing.T, src iq.Source, d *DDC, block int) []complex128 {
	t.Helper()
	var out []complex128
	buf := make([]complex64, block)
	for {
		n, err := src.Read(buf)
		out = append(out, d.Process(buf[:n])...)
		if err != nil {
			return out
		}
	}
}

func relRMS(got []complex128, want []complex64) float64 {
	var num, den float64
	for i := range want {
		w := complex128(want[i])
		num += real((got[i] - w) * cmplx.Conj(got[i]-w))
		den += real(w * cmplx.Conj(w))
	}
	return math.Sqrt(num / den)
}

// Reference DDC output on real signals: I/Q (nominal, 518 kHz) and real audio (usb_real).
func TestGoldenDDC(t *testing.T) {
	type meta struct {
		Channels map[string]struct {
			ShiftHz float64 `json:"shift_hz"`
			Decim   int     `json:"decimation"`
		} `json:"channels"`
	}
	for _, c := range []struct{ name, ref string }{{"nominal", "nominal_518_ddc.c64"}, {"usb_real", "usb_real_518_ddc.c64"}} {
		t.Run(c.name, func(t *testing.T) {
			var m meta
			testvec.JSON(t, c.name+".json", &m)
			ch := m.Channels["518000"]
			want := testvec.C64(t, c.ref)
			for _, block := range []int{1, 4096, 100003} {
				if block == 1 && len(want) > 100000 {
					continue // covered by the invariance test below; keep this one fast
				}
				var m2 struct {
					CentreHz float64 `json:"centre_hz"`
				}
				testvec.JSON(t, c.name+".json", &m2)
				w, err := iq.OpenWav(testvec.Path(t, c.name+".wav"), iq.WavOptions{CentreHz: m2.CentreHz, HasCentre: true})
				if err != nil {
					t.Fatal(err)
				}
				fs := w.SampleRate()
				if got := PickDecim(fs, 4000); got != ch.Decim {
					t.Fatalf("PickDecim = %d, reference %d", got, ch.Decim)
				}
				d := New(fs, 518000-m2.CentreHz, ch.Decim)
				if math.Abs(518000-m2.CentreHz-ch.ShiftHz) > 0 {
					t.Fatalf("shift %v vs reference %v", 518000-m2.CentreHz, ch.ShiftHz)
				}
				got := drain(t, w, d, block)
				w.Close()
				if len(got) != len(want) {
					t.Fatalf("block %d: %d samples, reference %d", block, len(got), len(want))
				}
				if e := relRMS(got, want); e > 1e-6 {
					t.Errorf("block %d: relative RMS error %g", block, e)
				}
			}
		})
	}
}

// Any block partition must give bit-identical output.
func TestBlockInvariance(t *testing.T) {
	mk := func(block int) []complex128 {
		w, err := iq.OpenWav(testvec.Path(t, "nominal.wav"), iq.WavOptions{CentreHz: 504000, HasCentre: true})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		// limit to the first ~300k frames to keep block=1 quick
		var out []complex128
		d := New(w.SampleRate(), 14000, 16)
		buf := make([]complex64, block)
		total := 0
		for total < 300000 {
			n, err := w.Read(buf)
			if n > 300000-total {
				n = 300000 - total
			}
			out = append(out, d.Process(buf[:n])...)
			total += n
			if err != nil {
				break
			}
		}
		return out
	}
	ref := mk(65536)
	for _, b := range []int{1, 7, 4096, 100003} {
		got := mk(b)
		if !reflect.DeepEqual(got, ref) {
			t.Fatalf("block size %d differs from 65536", b)
		}
	}
}

// Real audio: a tone 50 Hz above the shift must appear at +50 Hz with amplitude 0.5, and the mirror image
// (which lands at -3050 Hz before decimation) must be rejected by the decimator.
func TestRealAudioImageRejection(t *testing.T) {
	const fs, shift, off = 8000.0, 1500.0, 50.0
	d := New(fs, shift, 2)
	n := 40000
	in := make([]complex64, n)
	for i := range in {
		in[i] = complex(float32(math.Cos(2*math.Pi*(shift+off)*float64(i)/fs)), 0)
	}
	out := d.Process(in)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range out[200:] { // skip filter warm-up
		m := cmplx.Abs(v)
		lo, hi = math.Min(lo, m), math.Max(hi, m)
	}
	if lo < 0.5-2e-3 || hi > 0.5+2e-3 {
		t.Fatalf("|out| ranges %g..%g, want 0.5 +/- 2e-3 (image leakage?)", lo, hi)
	}
}

func TestMixerPhaseContinuity(t *testing.T) {
	m := NewMixer(64000, 14000)
	in := make([]complex64, 1000)
	for i := range in {
		in[i] = 1
	}
	out := make([]complex128, 1000)
	m.Process(in[:333], out[:333])
	m.Process(in[333:], out[333:])
	for i, v := range out {
		want := cmplx.Exp(complex(0, -2*math.Pi*14000*float64(i)/64000))
		if cmplx.Abs(v-want) > 1e-12 {
			t.Fatalf("sample %d: %v want %v", i, v, want)
		}
	}
}
