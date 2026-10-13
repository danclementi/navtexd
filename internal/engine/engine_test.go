package engine

import (
	"io"
	"math"
	"math/rand"
	"strings"
	"testing"

	"navtex/internal/config"
	"navtex/internal/iq"
	"navtex/internal/segment"
	"navtex/internal/sitor"
	"navtex/internal/testvec"
)

type result struct {
	records []segment.Record
	meters  []Meters
	status  []string
	text    strings.Builder
}

func open(t *testing.T, name string, centre float64) iq.Source {
	t.Helper()
	w, err := iq.OpenWav(testvec.Path(t, name+".wav"), iq.WavOptions{CentreHz: centre, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func newEngine(t *testing.T, src iq.Source, cfg config.Config, r *result) *Engine {
	t.Helper()
	e, err := New(src.SampleRate(), src.CentreHz(), src.Kind(), cfg, Handlers{
		Text:   func(s string, _ float64) { r.text.WriteString(s) },
		Status: func(s string) { r.status = append(r.status, s) },
		Meters: func(m Meters) { r.meters = append(r.meters, m) },
		Record: func(rec segment.Record) { r.records = append(r.records, rec) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// drive feeds src through e in blocks; before(sec) is called ahead of the first block at or after the given time.
func drive(e *Engine, src iq.Source, block int, hook func(now float64)) {
	buf := make([]complex64, block)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if hook != nil {
				hook(e.Now())
			}
			e.Process(buf[:n])
		}
		if err == io.EOF {
			break
		}
	}
	e.Flush()
}

func cfg518() config.Config {
	c := config.Default()
	c.Channel.FrequencyHz = 518000
	return c
}

func TestRecordsMetersAndSync(t *testing.T) {
	src := open(t, "clean", 504000)
	var r result
	e := newEngine(t, src, cfg518(), &r)
	drive(e, src, 4096, nil)
	if len(r.records) == 0 || r.records[0].Kind != segment.Message {
		t.Fatalf("records: %+v", r.records)
	}
	rec := r.records[0]
	if rec.B1 == "" || rec.Serial == "" || rec.Body() == "" || rec.Lines[0].Text[:4] != "ZCZC" || rec.Quality < 30 {
		t.Errorf("record %+v", rec)
	}
	full, data, spectra := 0, 0, 0
	var maxQ float64
	for _, m := range r.meters {
		if m.Sync == SyncFull {
			full++
		}
		if m.Data {
			data++
		}
		if m.Spectrum != nil {
			spectra++
		}
		maxQ = math.Max(maxQ, m.Quality)
		if m.Level.Peak <= 0 || m.Level.RMS <= 0 || m.Level.RMS > m.Level.Peak {
			t.Fatalf("level %+v", m.Level)
		}
	}
	if len(r.meters) < 20 || full == 0 || data == 0 || spectra == 0 || maxQ < 40 {
		t.Errorf("meters %d, sync %d, data %d, spectra %d, best quality %.0f", len(r.meters), full, data, spectra, maxQ)
	}
	t.Logf("%d meter reports, %d in sync, best quality %.0f, record quality %.0f", len(r.meters), full, maxQ, rec.Quality)
}

func TestRealAudioSpectrumPeaks(t *testing.T) {
	src := open(t, "usb_real", 516500) // carrier at 1500 Hz in the audio
	var r result
	c := cfg518()
	e := newEngine(t, src, c, &r)
	drive(e, src, 2048, nil)
	var sp *Spectrum
	for i := len(r.meters) - 1; i >= 0 && sp == nil; i-- {
		if r.meters[i].Sync == SyncFull {
			sp = r.meters[i].Spectrum
		}
	}
	if sp == nil {
		t.Fatal("no spectrum while in sync")
	}
	if sp.StartHz != 0 || sp.StepHz <= 0 || sp.StepHz > 8 || len(sp.Filtered) != len(sp.Raw) {
		t.Fatalf("frame axis %+v", sp)
	}
	// the highest filtered bin lies within the FSK tone pair around the carrier
	best := 0
	for i, v := range sp.Filtered {
		if v > sp.Filtered[best] {
			best = i
		}
	}
	f := sp.StartHz + float64(best)*sp.StepHz
	if math.Abs(f-sp.CarrierHz) > 120 || math.Abs(sp.CarrierHz-1500) > 20 {
		t.Errorf("peak at %.0f Hz, carrier %.1f Hz (expected about 1500)", f, sp.CarrierHz)
	}
}

func TestNoiseQualityIsLow(t *testing.T) {
	// 60 s of white noise at 48 kHz, real audio
	rng := rand.New(rand.NewSource(1))
	fs := 48000.0
	var r result
	c := cfg518()
	e, err := New(fs, 517000, iq.Real, c, Handlers{Meters: func(m Meters) { r.meters = append(r.meters, m) }})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]complex64, 4800)
	for i := 0; i < 600; i++ {
		for j := range buf {
			buf[j] = complex(float32(rng.NormFloat64()*0.1), 0)
		}
		e.Process(buf)
	}
	e.Flush()
	var sum float64
	n := 0
	for _, m := range r.meters[len(r.meters)/2:] {
		sum += m.Quality
		n++
		if m.Sync == SyncFull {
			t.Fatal("sync on noise")
		}
	}
	if avg := sum / float64(n); avg > 15 {
		t.Errorf("noise quality %.1f, want near 0", avg)
	}
}

func TestControls(t *testing.T) {
	// forcing the wrong polarity prevents decoding; the right one works
	for _, tc := range []struct {
		pol  sitor.Polarity
		want bool
	}{{sitor.PolNormal, true}, {sitor.PolInverted, false}} {
		src := open(t, "clean", 504000)
		var r result
		c := cfg518()
		c.Decoder.Polarity = tc.pol
		e := newEngine(t, src, c, &r)
		drive(e, src, 8192, nil)
		got := false
		for _, rec := range r.records {
			if rec.Kind == segment.Message && rec.Body() != "" {
				got = true
			}
		}
		if got != tc.want {
			t.Errorf("polarity %v: decoded=%v want %v", tc.pol, got, tc.want)
		}
	}

	// resync mid-stream: decoding recovers and the message is still found
	src := open(t, "clean", 504000)
	var r result
	e := newEngine(t, src, cfg518(), &r)
	done := false
	drive(e, src, 8192, func(now float64) {
		if !done && now > 25 {
			done = true
			e.Resync()
			e.SetUnshiftOnSpace(true)
			e.SetUnshiftOnSpace(false)
		}
	})
	joined := strings.Join(r.status, "|")
	if !strings.Contains(joined, "resync requested") || !strings.Contains(joined, "unlock") || !strings.Contains(joined, "unshift on space: ON") {
		t.Errorf("status: %v", r.status)
	}
	if len(r.records) == 0 {
		t.Error("nothing decoded after resync")
	}
}

func TestAutotuneOffKeepsCarrier(t *testing.T) {
	src := open(t, "usb_real", 516500)
	var r result
	c := cfg518()
	c.Input.Device.Autotune = false
	c.Input.Device.ManualCarrierHz = 1500
	e := newEngine(t, src, c, &r)
	drive(e, src, 2048, nil)
	if len(r.records) == 0 {
		t.Fatal("no record with a correct manual carrier")
	}
	last := r.meters[len(r.meters)-1]
	if last.Carrier.Autotune || math.Abs(last.Carrier.AudioHz-1500) > 0.01 {
		t.Errorf("carrier %+v", last.Carrier)
	}
	// a carrier set 60 Hz away decodes nothing useful, and no tracking happens
	src2 := open(t, "usb_real", 516500)
	var r2 result
	c.Input.Device.ManualCarrierHz = 1560
	e2 := newEngine(t, src2, c, &r2)
	drive(e2, src2, 2048, nil)
	for _, s := range r2.status {
		if strings.Contains(s, "tracking") || strings.Contains(s, "carrier found") {
			t.Errorf("autotune off but: %s", s)
		}
	}
}

// Changing the live options in the middle of a stream must not disturb decoding: the text is identical to an
// undisturbed run and the engine keeps running (no restart is involved at all).
func TestReconfigureMidStream(t *testing.T) {
	run := func(change func(*config.Config)) string {
		src := open(t, "clean", 504000)
		var r result
		c := cfg518()
		e := newEngine(t, src, c, &r)
		done := false
		drive(e, src, 4096, func(now float64) {
			if !done && now > 3 {
				done = true
				n := c
				change(&n)
				if config.RestartNeeded(c, n) {
					t.Fatalf("change should not need a restart")
				}
				e.Reconfigure(n)
			}
		})
		return r.text.String()
	}
	base := run(func(*config.Config) {})
	if len(base) < 50 {
		t.Fatalf("no text decoded: %q", base)
	}
	changes := map[string]func(*config.Config){
		"meters": func(c *config.Config) {
			c.Meters.SpectrumSmooth, c.Meters.SpectrumFFT, c.Meters.RateHz, c.Meters.QualityWindowS = 0.5, 1024, 20, 1
		},
		"segment": func(c *config.Config) { c.Segment.IdleCloseS, c.Segment.MaxLineChars = 30, 120 },
		"decisions": func(c *config.Config) {
			c.Decoder.MinRun, c.Decoder.MaxBad, c.Decoder.VoteAgree, c.Decoder.KeepParity = 6, 8, 3, 100
		},
		"receiver": func(c *config.Config) {
			c.Receiver.BlankK, c.Receiver.SearchHopS, c.Receiver.TrackGain, c.Receiver.PresentDB, c.Receiver.PresentOff = 8, 2, 0.5, 2, 300
			c.Receiver.Alpha, c.Receiver.ConfThr, c.Receiver.MaxLost, c.Receiver.LoseAfterS = 0.3, 0.02, 4, 15
		},
		"format": func(c *config.Config) { c.Decoder.Unreadable = sitor.Star },
	}
	for name, ch := range changes {
		if got := run(ch); got != base {
			t.Errorf("%s: text changed:\n%q\nwant\n%q", name, got, base)
		}
	}
}

// The channel trace shows the tone inside the channel filter at the input level, rejects a tone outside it, .
func TestChannelSpectrum(t *testing.T) {
	const fs = 48000.0
	var r result
	e, err := New(fs, 516500, iq.Real, cfg518(), Handlers{Meters: func(m Meters) { r.meters = append(r.meters, m) }})
	if err != nil {
		t.Fatal(err)
	}
	e.SetAutotune(false)
	e.SetCarrierAudioHz(1500)
	buf := make([]complex64, 4096)
	n := 0
	for blk := 0; blk < 12*int(fs)/len(buf); blk++ {
		for i := range buf {
			tm := float64(n) / fs
			buf[i] = complex(float32(0.3*math.Sin(2*math.Pi*1500*tm)+0.3*math.Sin(2*math.Pi*900*tm)), 0)
			n++
		}
		e.Process(buf)
	}
	var sp *Spectrum
	for i := len(r.meters) - 1; i >= 0 && sp == nil; i-- {
		if s := r.meters[i].Spectrum; s != nil && s.Channel != nil {
			sp = s
		}
	}
	if sp == nil {
		t.Fatal("no channel spectrum")
	}
	if len(sp.Channel) != len(sp.Filtered) {
		t.Fatalf("channel trace has %d bins, input %d", len(sp.Channel), len(sp.Filtered))
	}
	at := func(hz float64) int { return int(math.Round((hz - sp.StartHz) / sp.StepHz)) }
	in, ch := sp.Filtered[at(1500)], sp.Channel[at(1500)]
	t.Logf("1500 Hz: input %.1f dB, channel %.1f dB; 900 Hz: input %.1f, channel %.1f", in, ch,
		sp.Filtered[at(900)], sp.Channel[at(900)])
	if math.Abs(float64(ch-in)) > 3 {
		t.Errorf("tone inside the filter: input %.1f dB, channel %.1f dB", in, ch)
	}
	if rej := sp.Channel[at(900)]; rej > in-40 {
		t.Errorf("tone outside the filter is only %.1f dB down in the channel trace", float64(in-rej))
	}
}

// Bins of the input grid outside the filtered stream's bandwidth are reported as blank.
func TestChannelSpectrumBlankOutsideBand(t *testing.T) {
	c := newChanSpec(4000, 6, 8192, 0.5)
	c.feed(make([]complex128, c.n))
	out := make([]float32, 1000) // 0..6000 Hz in 6 Hz steps, channel centred at 1500 Hz
	if !c.frame(out, 0, 6, 1500) {
		t.Fatal("no frame")
	}
	for j, v := range out {
		f := float64(j) * 6
		if (math.Abs(f-1500) >= 1920) != (v <= chanOff) {
			t.Fatalf("bin at %.0f Hz = %.1f", f, v)
		}
	}
}
