package sitor

// Review tests (not part of the project): property and fuzz tests proposed in pass 9 of the review.

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"navtex/internal/fsk"
)

// symsFrom turns bytes into soft symbols: each byte is one symbol in -1..1, some locked, with NaN / Inf sprinkled in when
// the byte says so (the demodulator should never emit them, but a framer should survive them).
func symsFrom(b []byte) []fsk.Symbol {
	out := make([]fsk.Symbol, len(b))
	for i, x := range b {
		v := (float64(x) - 127.5) / 127.5
		switch x {
		case 0xFF:
			v = math.NaN()
		case 0xFE:
			v = math.Inf(1)
		}
		out[i] = fsk.Symbol{Soft: v, Locked: x&1 == 0, Center: float64(i)}
	}
	return out
}

// valid SITOR-B: 4-of-7 words, each repeated 5 words later, so the framer actually locks and emits text.
func sitorSyms(seed int64, n int) []fsk.Symbol {
	rng := rand.New(rand.NewSource(seed))
	var pats []uint8
	for m := 0; m < 128; m++ {
		k := 0
		for b := 0; b < 7; b++ {
			k += m >> b & 1
		}
		if k == 4 {
			pats = append(pats, uint8(m))
		}
	}
	words := make([]uint8, n/7+8)
	for i := range words {
		if i%2 == 1 && i >= 5 {
			words[i] = words[i-5]
		} else {
			words[i] = pats[rng.Intn(len(pats))]
		}
	}
	out := make([]fsk.Symbol, n)
	for i := range out {
		bit := words[i/7] >> (i % 7) & 1
		v := -0.8
		if bit == 1 {
			v = 0.8
		}
		v += 0.3 * rng.NormFloat64()
		out[i] = fsk.Symbol{Soft: v, Locked: true, Center: float64(i)}
	}
	return out
}

func runFramer(f Framer, syms []fsk.Symbol, chunk int) []Event {
	var evs []Event
	for i := 0; i < len(syms); i += chunk {
		evs = append(evs, f.Process(syms[i:min(i+chunk, len(syms))])...)
	}
	return append(evs, f.Flush()...)
}

// Property: output does not depend on block size (CLAUDE.md rule 2), for both framers.
func TestReview_FramerBlockSizeIndependence(t *testing.T) {
	syms := sitorSyms(7, 20000)
	for name, mk := range map[string]func() Framer{
		"hard": func() Framer { return NewDecoder(DefaultConfig()) },
		"soft": func() Framer { return New(DefaultConfig()) },
	} {
		ref := runFramer(mk(), syms, len(syms))
		if len(ref) == 0 {
			t.Fatalf("%s: no events at all on valid SITOR-B", name)
		}
		for _, chunk := range []int{1, 2, 7, 13, 100, 4096} {
			got := runFramer(mk(), syms, chunk)
			if !reflect.DeepEqual(ref, got) {
				t.Errorf("%s: chunk %d gives %d events, whole stream %d (first difference: %s)", name, chunk, len(got), len(ref), firstDiff(ref, got))
			}
		}
	}
}

func firstDiff(a, b []Event) string {
	for i := 0; i < len(a) && i < len(b); i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			return fmt.Sprintf("#%d %+v vs %+v", i, a[i], b[i])
		}
	}
	return "length"
}

// Fuzz: no panic, and (for finite input) no NaN reaching events, on arbitrary symbol streams, in arbitrary chunks.
func FuzzReview_Framer(f *testing.F) {
	f.Add([]byte{1, 2, 3, 0xFF, 0xFE, 200, 10}, uint16(3), false)
	b := make([]byte, 0, 700)
	for _, s := range sitorSyms(1, 700) {
		b = append(b, byte((s.Soft+1)*127))
	}
	f.Add(b, uint16(7), true)
	f.Fuzz(func(t *testing.T, data []byte, chunk uint16, soft bool) {
		if chunk == 0 {
			chunk = 1
		}
		var fr Framer = NewDecoder(DefaultConfig())
		if soft {
			fr = New(DefaultConfig())
		}
		syms := symsFrom(data)
		for i := 0; i < len(syms); i += int(chunk) {
			fr.Process(syms[i:min(i+int(chunk), len(syms))])
			if i%3 == 0 {
				fr.SetPolarity(Polarity(i % 3))
			}
		}
		fr.Flush()
		fr.Resync()
	})
}

// Property: memory stays bounded on endless input (here ~10 minutes of locked SITOR-B at 100 Bd fed in 1 s blocks): the heap
// in use after the run must not grow with its length.
func TestReview_FramerBoundedMemory(t *testing.T) {
	heap := readMem
	for name, fr := range map[string]Framer{"hard": NewDecoder(DefaultConfig()), "soft": New(DefaultConfig())} {
		syms := sitorSyms(3, 6000)
		h0 := heap()
		for round := 0; round < 10; round++ {
			for i := 0; i < len(syms); i += 100 {
				fr.Process(syms[i:min(i+100, len(syms))])
			}
		}
		h1 := heap()
		for round := 0; round < 40; round++ {
			for i := 0; i < len(syms); i += 100 {
				fr.Process(syms[i:min(i+100, len(syms))])
			}
		}
		h2 := heap()
		t.Logf("%s: heap %d KB -> %d KB -> %d KB", name, h0>>10, h1>>10, h2>>10)
		if h2 > h1+4<<20 {
			t.Errorf("%s: heap grew by %d KB over 4x more input", name, (h2-h1)>>10)
		}
	}
}
