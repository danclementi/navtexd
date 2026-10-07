package engine

import (
	"math"
	"testing"

	"navtex/internal/fsk"
)

func TestScopeTrigger(t *testing.T) {
	const fs, baud = 4000.0, 100.0
	sc := NewScope(fs, baud, 170, 20)
	sps := fs / baud
	// 3 s of a clean alternating-tone signal (bits 1,1,0,0,...), symbols centred on the sample grid
	nsym := 300
	x := make([]complex128, int(float64(nsym)*sps))
	for i := range x {
		bit := (i / int(sps) / 2) % 2 // 0,0,1,1,...
		f := -85.0
		if bit == 1 {
			f = 85
		}
		x[i] = complex(math.Cos(2*math.Pi*f*float64(i)/fs), math.Sin(2*math.Pi*f*float64(i)/fs))
	}
	sc.Block(0, x, x)
	var syms []fsk.Symbol
	for m := 0; m < nsym-2; m++ {
		bit := (m / 2) % 2
		soft := -80.0
		if bit == 1 {
			soft = 80
		}
		syms = append(syms, fsk.Symbol{Soft: soft, Locked: true, Center: (float64(m) + 0.5) * sps})
	}
	sc.Symbols(syms)
	sw := sc.Sweep(0.2, TrigRise, 0.1, 0.5, 1000)
	if sw == nil {
		t.Fatal("no sweep")
	}
	if sw.Trigger < 0 {
		t.Fatal("no trigger")
	}
	k := int(sw.Trigger * sw.Fs)
	if sw.Bit[k-3] != -1 || sw.Bit[k+3] != 1 {
		t.Errorf("bits around the trigger: %d %d", sw.Bit[k-3], sw.Bit[k+3])
	}
	if d := sw.Trigger - 0.1*sw.Span; math.Abs(d) > 1e-6 {
		t.Errorf("trigger not at 10%%: %v", sw.Trigger)
	}
	// the next sweep must start at or after the end of this one
	sw2 := sc.Sweep(0.2, TrigRise, 0.1, sw.T0+sw.Span, 1000)
	if sw2 == nil || sw2.T0 < sw.T0+sw.Span-1e-9 {
		t.Errorf("second sweep %+v", sw2)
	}
	if hi := sw.Traces["hi"]; len(hi) != len(sw.Bit) {
		t.Errorf("lane lengths differ")
	}
}
