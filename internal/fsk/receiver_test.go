package fsk

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"testing"

	"navtex/internal/ddc"
	"navtex/internal/iq"
	"navtex/internal/testvec"
)

type rxCase struct {
	name string
	chHz float64
}

func allCases() []rxCase {
	var out []rxCase
	for _, n := range []string{"clean", "nominal", "lowmark", "msb", "impulsive", "weak"} {
		out = append(out, rxCase{n, 518000}, rxCase{n, 490000})
	}
	return append(out, rxCase{"usb_real", 518000})
}

type rxMeta struct {
	CentreHz float64 `json:"centre_hz"`
	Channels map[string]struct {
		F0Refined float64 `json:"f0_refined_hz"`
		Fs2       float64 `json:"fs2"`
		Decim     int     `json:"decimation"`
		NLocked   int     `json:"n_locked_symbols"`
	} `json:"channels"`
}

// runReceiver streams a case's WAV through DDC and Receiver with the given read block size.
func runReceiver(t *testing.T, c rxCase, block int) (*Receiver, []Symbol, float64, int) {
	var m rxMeta
	testvec.JSON(t, c.name+".json", &m)
	ch := m.Channels[fmt.Sprint(int(c.chHz))]
	w, err := iq.OpenWav(testvec.Path(t, c.name+".wav"), iq.WavOptions{CentreHz: m.CentreHz, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	d := ddc.New(w.SampleRate(), c.chHz-m.CentreHz, ch.Decim)
	r := NewReceiver(DefaultReceiverConfig(ch.Fs2))
	var syms []Symbol
	buf := make([]complex64, block)
	for {
		n, err := w.Read(buf)
		syms = append(syms, r.Process(d.Process(buf[:n]))...)
		if err != nil {
			break
		}
	}
	syms = append(syms, r.Flush()...)
	return r, syms, ch.F0Refined, ch.NLocked
}

// The streaming front end (search + tracking instead of the reference's whole-record estimate) must acquire
// quickly, lock about as many symbols as the reference, and settle near the reference carrier offset.
func TestReceiverAcquisition(t *testing.T) {
	for _, c := range allCases() {
		t.Run(fmt.Sprintf("%s_%.0f", c.name, c.chHz/1e3), func(t *testing.T) {
			r, syms, refF0, refLocked := runReceiver(t, c, 8192)
			locked, first := 0, -1
			for i, s := range syms {
				if s.Locked {
					locked++
					if first < 0 {
						first = i
					}
				}
			}
			if first < 0 || first > 800 { // 8 s
				t.Errorf("first locked symbol at %d (want < 800)", first)
			}
			if float64(locked) < 0.95*float64(refLocked) {
				t.Errorf("locked %d symbols, reference %d", locked, refLocked)
			}
			var f []float64
			for _, ch := range r.Changes {
				if !ch.Search && ch.Sample > 12*4000 {
					f = append(f, ch.F0)
				}
			}
			if len(f) == 0 {
				t.Fatal("no tracking updates after 12 s")
			}
			sort.Float64s(f)
			if med := f[len(f)/2]; math.Abs(med-refF0) > 4 {
				t.Errorf("tracked f0 median %.2f Hz, reference %.2f Hz", med, refF0)
			}
			t.Logf("locked %d/%d, first@%d, %d f0 changes", locked, refLocked, first, len(r.Changes))
		})
	}
}

// Any partition of the input must give bit-identical symbols.
func TestReceiverBlockInvariance(t *testing.T) {
	c := rxCase{"nominal", 518000}
	_, ref, _, _ := runReceiver(t, c, 65536)
	for _, b := range []int{1000, 4097, 100003} {
		if _, got, _, _ := runReceiver(t, c, b); !reflect.DeepEqual(got, ref) {
			t.Fatalf("read block %d differs from 65536", b)
		}
	}
}

// Noise only: no carrier may be accepted and nothing may lock.
func TestReceiverNoiseOnly(t *testing.T) {
	fs := 4000.0
	r := NewReceiver(DefaultReceiverConfig(fs))
	x := make([]complex128, int(60*fs))
	seed := uint64(12345)
	next := func() float64 { // xorshift64* -> approx gaussian via sum of uniforms
		var s float64
		for i := 0; i < 12; i++ {
			seed ^= seed >> 12
			seed ^= seed << 25
			seed ^= seed >> 27
			s += float64((seed*2685821657736338717)>>11) / (1 << 53)
		}
		return s - 6
	}
	for i := range x {
		x[i] = complex(next(), next())
	}
	syms := append(r.Process(x), r.Flush()...)
	for i, s := range syms {
		if s.Locked {
			t.Fatalf("symbol %d locked on noise", i)
		}
	}
	if r.HaveCarrier() {
		t.Error("carrier accepted on noise")
	}
}
