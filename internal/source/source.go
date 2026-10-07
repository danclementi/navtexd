// Package source opens the configured sample source: a WAV file, a sound-card / virtual-cable input, or standard input.
package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"navtex/internal/capture"
	"navtex/internal/clip"
	"navtex/internal/config"
	"navtex/internal/iq"
)

// Opened is an open source. Start is the UTC time of the first sample of a recording; zero means live (system clock).
type Opened struct {
	iq.Source
	Start time.Time
	Info  string // one-line description for logs
	Close func() error
	Live  bool
}

// Open opens the source named by cfg.Input. Cancelling ctx unblocks a waiting read on a live source.
func Open(ctx context.Context, cfg config.Config) (*Opened, error) {
	switch cfg.Input.Source {
	case "file":
		if cfg.Input.File == "" {
			return nil, fmt.Errorf("no input file (input.file)")
		}
		meta, haveMeta := clip.ReadMeta(cfg.Input.File) // a saved clip carries its start time and centre frequency
		centre := cfg.Input.CentreHz
		if centre == 0 && haveMeta {
			centre = meta.CentreHz
		}
		w, err := iq.OpenWav(cfg.Input.File, iq.WavOptions{SwapIQ: cfg.Input.SwapIQ, CentreHz: centre, HasCentre: centre != 0})
		if err != nil {
			return nil, err
		}
		dur := float64(w.Frames()) / w.SampleRate()
		o := &Opened{Source: w, Close: w.Close}
		start, how, err := cfg.StartTime(dur)
		if t, perr := time.Parse(clip.TimeFormat, meta.StartUTC); haveMeta && strings.TrimSpace(cfg.Input.StartUTC) == "" && perr == nil {
			start, how, err = t.UTC(), "the clip's .json file", nil
		}
		if err != nil {
			o.Info = fmt.Sprintf("warning: cannot determine the recording start time: %v", err)
		} else {
			o.Start = start
			o.Info = fmt.Sprintf("recording start %s UTC (from %s), duration %.0f s", start.Format(config.TimeFormat), how, dur)
		}
		return o, nil
	case "device", "stdin":
		format, _ := iq.ParsePCMFormat(cfg.Input.PCMFormat)
		var r io.Reader = os.Stdin
		what := "standard input"
		if cfg.Input.Source == "device" {
			c, err := capture.Open(cfg.Input.Device, cfg.Input.SampleRateHz, cfg.Input.Channels)
			if err != nil {
				return nil, err
			}
			r, format = c, iq.S16LE
			what = "audio input " + strings.TrimSpace(cfg.Input.Device)
			if strings.TrimSpace(cfg.Input.Device) == "" {
				what = "the default audio input"
			}
		}
		p, err := iq.NewPCM(r, iq.PCMOptions{Rate: cfg.Input.SampleRateHz, Channels: cfg.Input.Channels, Format: format,
			CentreHz: cfg.LiveCentreHz(), SwapIQ: cfg.Input.SwapIQ})
		if err != nil {
			return nil, err
		}
		go func() { <-ctx.Done(); p.Close() }()
		o := &Opened{Source: p, Close: p.Close, Live: true}
		if t, err := config.ParseStart(cfg.Input.StartUTC); err == nil && !t.IsZero() {
			o.Start, o.Live = t, false // a piped recording with a given start time is stamped like a file
		}
		o.Info = fmt.Sprintf("listening to %s: %.0f Hz, %d channel(s), channel %.1f kHz, receiver LO %.1f kHz (carrier expected near %.0f Hz)",
			what, cfg.Input.SampleRateHz, cfg.Input.Channels, cfg.Channel.FrequencyHz/1e3, cfg.LiveCentreHz()/1e3, cfg.Channel.FrequencyHz-cfg.LiveCentreHz())
		return o, nil
	}
	return nil, fmt.Errorf("input.source %q: want file, device or stdin", cfg.Input.Source)
}
