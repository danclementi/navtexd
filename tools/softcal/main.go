// Command softcal measures the statistics of the soft-lock comparison scores on noise that has gone through navtexd's
// own receiver chain, and prints them next to the values the soft lock assumes (taken from navtex-rs).
//
//	go run ./tools/softcal [-seconds 3600] [-fs 4000] [-seed 1]
//
// It runs twice: "ungated" (presence and timing gates disabled, so every symbol carries a soft value) and "default"
// (the receiver as configured in navtexd, where soft values are zeroed whenever timing is not locked).
package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"

	"navtex/internal/fsk"
	"navtex/internal/softlock"
)

func main() {
	secs := flag.Float64("seconds", 3600, "seconds of noise")
	fs := flag.Float64("fs", 4000, "receiver input sample rate (Hz)")
	seed := flag.Int64("seed", 1, "random seed")
	conf := flag.Float64("conf", 0, "timing confidence threshold for the gated run (0 = the default)")
	only := flag.Bool("gated-only", false, "skip the ungated run")
	flag.Parse()
	for _, gated := range []bool{false, true} {
		if *only && !gated {
			continue
		}
		run(*secs, *fs, *seed, gated, *conf)
	}
}

func run(secs, fs float64, seed int64, gated bool, conf float64) {
	cfg := fsk.DefaultReceiverConfig(fs)
	name := "default (gated)"
	if !gated {
		name = "ungated"
		cfg.PresentDB = -100
		cfg.ConfThr = 0
		cfg.MaxLost = 1 << 30
	}
	if gated && conf > 0 {
		cfg.ConfThr = conf
		name = fmt.Sprintf("default (gated), timing_confidence %.3f", conf)
	}
	r := fsk.NewReceiver(cfg)
	r.SetAutotune(false)
	r.SetCarrier(0)
	rng := rand.New(rand.NewSource(seed))
	n := int(secs * fs)
	buf := make([]complex128, 4000)
	var soft []float64
	zero, total := 0, 0
	half := cfg.ShiftHz / 2
	for done := 0; done < n; done += len(buf) {
		for i := range buf {
			buf[i] = complex(rng.NormFloat64(), rng.NormFloat64())
		}
		for _, s := range r.Process(buf) {
			v := s.Soft / half
			v = math.Max(-1.5, math.Min(1.5, v))
			if v == 0 {
				zero++
			}
			total++
			soft = append(soft, v)
		}
	}
	p := softlock.DefaultParams()
	rep := softlock.MeasureNull(soft, p)
	fmt.Printf("== %s: %d symbols (%.0f s), %.1f%% zero\n", name, total, float64(total)/100, 100*float64(zero)/math.Max(1, float64(total)))
	fmt.Printf("   cosine      mean %.4f sd %.4f   (assumed %.4f %.4f)\n", rep.Cosine[0], rep.Cosine[1], p.CosineNull[0], p.CosineNull[1])
	fmt.Printf("   correlation mean %.4f sd %.4f   (assumed %.4f %.4f)\n", rep.Correlation[0], rep.Correlation[1], p.CorrelationNull[0], p.CorrelationNull[1])
	fmt.Printf("   score       mean %.4f sd %.4f   (assumed sd %.4f)\n", rep.ScoreMean, rep.ScoreSD, p.ScoreSD)
	fmt.Printf("   lag-1 autocorrelation %.4f; window z sd %.3f (ideal 1); max z %.2f; fraction >= z* %.2e; lock declarations %d\n",
		rep.Lag1, rep.ZSD, rep.ZMax, rep.FracOver, rep.Locks)
}
