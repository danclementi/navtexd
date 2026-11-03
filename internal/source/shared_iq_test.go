package source

import (
	"context"
	"io"
	"math"
	"math/rand"
	"sync"
	"testing"

	"navtex/internal/config"
	"navtex/internal/engine"
	"navtex/internal/iq"
)

// A synthetic stream like the one an OpenHPSDR radio delivers (I/Q, 48 kS/s), with an FSK carrier (100 baud, 170 Hz shift, valid
// SITOR-B words) at each of the given offsets from the stream's centre.
type iqStub struct {
	rate, centre float64
}

func (iqStub) Read([]complex64) (int, error) { return 0, io.EOF }
func (s iqStub) SampleRate() float64         { return s.rate }
func (s iqStub) CentreHz() float64           { return s.centre }
func (iqStub) Kind() iq.Kind                 { return iq.IQ }

func fskCarriers(rate float64, seconds float64, offsets []float64, seed int64) []complex64 {
	n := int(rate * seconds)
	out := make([]complex64, n)
	rng := rand.New(rand.NewSource(seed))
	const baud = 100.0
	sps := rate / baud
	for ci, f := range offsets {
		// 7-bit words with four 1s; every second word repeats the one five words earlier (the SITOR-B time diversity)
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
		words := make([]uint8, int(seconds*baud/7)+8)
		for i := range words {
			if i%2 == 1 && i >= 5 {
				words[i] = words[i-5]
			} else {
				words[i] = pats[rng.Intn(len(pats))]
			}
		}
		phase := 2 * math.Pi * rng.Float64()
		for i := 0; i < n; i++ {
			sym := int(float64(i) / sps)
			bit := words[sym/7] >> (sym % 7) & 1
			dev := -85.0
			if bit == 1 {
				dev = 85
			}
			phase += 2 * math.Pi * (f + dev) / rate
			out[i] += complex(float32(0.2*math.Cos(phase)), float32(0.2*math.Sin(phase)))
		}
		_ = ci
	}
	for i := range out {
		out[i] += complex(float32(rng.NormFloat64()*0.01), float32(rng.NormFloat64()*0.01))
	}
	return out
}

// run feeds one stream to one decoding channel per frequency through a Tap, as channels that share an input do, and reports
// for each whether it found its carrier and got into word sync.
func runShared(t *testing.T, rate, centre float64, freqs []float64, x []complex64) (found, synced []bool) {
	t.Helper()
	tap := NewTap(&Opened{Source: iqStub{rate, centre}, Close: func() error { return nil }})
	found, synced = make([]bool, len(freqs)), make([]bool, len(freqs))
	var wg sync.WaitGroup
	for i, f := range freqs {
		cfg := config.Default()
		cfg.Channel.FrequencyHz = f
		sub, err := Shared(tap, "channel 1", f)
		if err != nil {
			t.Fatal(err)
		}
		i := i
		e, err := engine.New(sub.SampleRate(), sub.CentreHz(), sub.Kind(), cfg, engine.Handlers{
			Meters: func(m engine.Meters) {
				if m.Sync == engine.SyncFull {
					synced[i] = true
				}
			},
		})
		if err != nil {
			t.Fatalf("channel %.1f kHz: %v", f/1e3, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]complex64, 4096)
			for {
				n, err := sub.Read(buf)
				if n > 0 {
					e.Process(buf[:n])
				}
				if err != nil {
					break
				}
			}
			e.Flush()
			found[i] = e.Summary().CarrierFound
		}()
	}
	for off := 0; off < len(x); off += 4096 {
		tap.Publish(context.Background(), x[off:min(off+4096, len(x))], true)
	}
	tap.Close()
	wg.Wait()
	return found, synced
}

// 518, 490 and 486 kHz fit one 48 kS/s stream when the stream is centred between them (near 502 kHz): the owner's lo_below_hz
// is then about 16000, not the default 1000.
func TestSharedStream48kHoldsThreeChannels(t *testing.T) {
	const rate, centre = 48000.0, 502000.0
	freqs := []float64{518000, 490000, 486000}
	offs := []float64{freqs[0] - centre, freqs[1] - centre, freqs[2] - centre}
	x := fskCarriers(rate, 40, offs, 1)
	found, synced := runShared(t, rate, centre, freqs, x)
	for i, f := range freqs {
		if !found[i] || !synced[i] {
			t.Errorf("%.0f kHz: carrier found %v, word sync %v", f/1e3, found[i], synced[i])
		}
	}
}

// With the default lo_below_hz (1000) the stream is centred at 517 kHz: 490 kHz is 27 kHz away, outside 48 kS/s, and the program
// refuses it, with a message that names the offset.
func TestSharedStream48kDefaultCentreRefusesFarChannel(t *testing.T) {
	cfg := config.Default()
	cfg.Channel.FrequencyHz = 490000
	if _, err := engine.New(48000, 517000, iq.IQ, cfg, engine.Handlers{}); err == nil {
		t.Fatal("490 kHz in a 48 kS/s stream centred at 517 kHz should be refused")
	}
}

// 96 kS/s holds 518 and 486 kHz around 502 kHz easily, but 424 kHz needs a wider stream (it is 94 kHz from 518 kHz).
func TestSharedStream96kAndTheEdge(t *testing.T) {
	cfg := config.Default()
	cfg.Channel.FrequencyHz = 424000
	if _, err := engine.New(96000, 471000, iq.IQ, cfg, engine.Handlers{}); err == nil {
		t.Error("424 kHz at the edge of a 96 kS/s stream centred at 471 kHz should be refused (usable +-46.5 kHz)")
	}
	cfg.Channel.FrequencyHz = 486000
	if _, err := engine.New(96000, 502000, iq.IQ, cfg, engine.Handlers{}); err != nil {
		t.Errorf("486 kHz in 96 kS/s around 502 kHz: %v", err)
	}
}
