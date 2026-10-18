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
	"navtex/internal/hpsdr"
	"navtex/internal/iq"
)

// Opened is an open source. Start is the UTC time of the first sample of a recording; zero means live (system clock).
type Opened struct {
	iq.Source
	Start time.Time
	Info  string // one-line description for logs
	Close func() error
	Live  bool
	// Shared is set for a channel that takes its samples from another channel's input (see Tap); it is paced by that input.
	Shared bool
	// Notices, if not nil, carries messages from a live source (lost network packets, the radio chosen) for the log.
	Notices <-chan Notice
}

// Notice is a log message from a live source; Level is "info" or "warn".
type Notice struct{ Level, Text string }

// Option adjusts Open.
type Option func(*env)

type env struct{ radios *hpsdr.MRU }

// WithRadios gives Open the list of radios used successfully: it prefers them when a search finds several, and adds a
// radio to it once the radio has delivered data.
func WithRadios(m *hpsdr.MRU) Option { return func(e *env) { e.radios = m } }

// Open opens the source named by cfg.Input. Cancelling ctx unblocks a waiting read on a live source.
func Open(ctx context.Context, cfg config.Config, opts ...Option) (*Opened, error) {
	var e env
	for _, o := range opts {
		o(&e)
	}
	switch cfg.Input.Source {
	case "file":
		fi := cfg.Input.File
		if fi.Path == "" {
			return nil, fmt.Errorf("no input file (input.file.path)")
		}
		meta, haveMeta := clip.ReadMeta(fi.Path) // a saved clip carries its start time and centre frequency
		centre := fi.CentreHz
		if centre == 0 && haveMeta {
			centre = meta.CentreHz
		}
		w, err := iq.OpenWav(fi.Path, iq.WavOptions{SwapIQ: fi.SwapIQ, CentreHz: centre, HasCentre: centre != 0})
		if err != nil {
			return nil, err
		}
		dur := float64(w.Frames()) / w.SampleRate()
		o := &Opened{Source: w, Close: w.Close}
		start, how, err := cfg.StartTime(dur)
		if t, perr := time.Parse(clip.TimeFormat, meta.StartUTC); haveMeta && strings.TrimSpace(fi.StartUTC) == "" && perr == nil {
			start, how, err = t.UTC(), "the clip's .json file", nil
		}
		if err != nil {
			o.Info = fmt.Sprintf("warning: cannot determine the recording start time: %v", err)
		} else {
			o.Start = start
			o.Info = fmt.Sprintf("recording start %s UTC (from %s), duration %.0f s", start.Format(config.TimeFormat), how, dur)
		}
		return o, nil
	case "hpsdr":
		notes := make(chan Notice, 8)
		say := func(level, text string) {
			select {
			case notes <- Notice{level, text}:
			default:
			}
		}
		centre := cfg.LiveCentreHz()
		warn := make(chan string, 8)
		go func() { // lost-packet warnings from the radio source
			for {
				select {
				case m := <-warn:
					say("warn", m)
				case <-ctx.Done():
					return
				}
			}
		}()
		h, err := hpsdr.Open(ctx, hpsdr.Options{Addr: cfg.Input.HPSDR.Address, RateHz: int(cfg.Input.HPSDR.SampleRateHz), FreqHz: centre,
			SwapIQ: cfg.Input.HPSDR.SwapIQ, Notices: warn, Known: e.radios.Addrs(),
			OnData: func(r hpsdr.Radio) {
				if err := e.radios.Touch(r.Addr.IP.String(), r.Name); err != nil {
					say("warn", "could not save the radio list: "+err.Error())
				}
			},
			Chosen: func(why string) { say("info", why) }})
		if err != nil {
			return nil, err
		}
		o := &Opened{Source: h, Close: h.Close, Live: true, Notices: notes}
		o.Info = fmt.Sprintf("radio %s, receiver %d: I/Q %.0f Hz, channel %.1f kHz, receiver LO %.1f kHz (carrier expected near %.0f Hz)",
			h.Radio(), h.Receiver(), cfg.Input.HPSDR.SampleRateHz, cfg.Channel.FrequencyHz/1e3, centre/1e3, cfg.Channel.FrequencyHz-centre)
		return o, nil
	case "device", "stdin":
		rate, channels, swap := cfg.Input.Stdin.SampleRateHz, cfg.Input.Stdin.Channels, cfg.Input.Stdin.SwapIQ
		format, _ := iq.ParsePCMFormat(cfg.Input.Stdin.PCMFormat)
		var r io.Reader = os.Stdin
		what := "standard input"
		if cfg.Input.Source == "device" {
			dev := cfg.Input.Device
			rate, channels, swap = dev.SampleRateHz, dev.Channels, dev.SwapIQ
			c, err := capture.Open(dev.Name, rate, channels)
			if err != nil {
				return nil, err
			}
			r, format = c, iq.S16LE
			what = "audio input " + strings.TrimSpace(dev.Name)
			if strings.TrimSpace(dev.Name) == "" {
				what = "the default audio input"
			}
		}
		p, err := iq.NewPCM(r, iq.PCMOptions{Rate: rate, Channels: channels, Format: format,
			CentreHz: cfg.LiveCentreHz(), SwapIQ: swap})
		if err != nil {
			return nil, err
		}
		go func() { <-ctx.Done(); p.Close() }()
		o := &Opened{Source: p, Close: p.Close, Live: true}
		if t, err := config.ParseStart(cfg.Input.Stdin.StartUTC); cfg.Input.Source == "stdin" && err == nil && !t.IsZero() {
			o.Start, o.Live = t, false // a piped recording with a given start time is stamped like a file
		}
		o.Info = fmt.Sprintf("listening to %s: %.0f Hz, %d channel(s), channel %.1f kHz, receiver LO %.1f kHz (carrier expected near %.0f Hz)",
			what, rate, channels, cfg.Channel.FrequencyHz/1e3, cfg.LiveCentreHz()/1e3, cfg.Channel.FrequencyHz-cfg.LiveCentreHz())
		return o, nil
	}
	return nil, fmt.Errorf("input.source %q: want file, device, stdin or hpsdr", cfg.Input.Source)
}
