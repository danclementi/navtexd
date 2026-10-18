// Command navtex decodes NAVTEX (518/490 kHz, SITOR-B) from a WAV recording.
//
// All options live in one configuration (internal/config). Start from the defaults, optionally load a JSON file with
// -config, then override single options with -set path=value or the shortcut flags below. -options lists every option;
// -dump-config prints the effective configuration as JSON. All times are UTC.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"navtex/internal/capture"
	"navtex/internal/config"
	"navtex/internal/pipeline"
	"navtex/internal/source"
	"navtex/internal/textout"
)

type setFlags []string

func (s *setFlags) String() string     { return strings.Join(*s, ",") }
func (s *setFlags) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var (
		cfgFile = flag.String("config", "", "JSON configuration file (see -dump-config); unset options keep their defaults")
		dump    = flag.Bool("dump-config", false, "print the effective configuration as JSON and exit")
		listDev = flag.Bool("list-devices", false, "list the audio inputs usable with -device and exit")
		options = flag.Bool("options", false, "list every option with its help text and exit")
		sets    setFlags
	)
	flag.Var(&sets, "set", "override any option: -set decoder.unreadable=star (repeatable)")
	// shortcuts for the most used options; each is equivalent to a -set
	shortcuts := map[string]string{
		"in":         "input.file.path",
		"device":     "input.device.name",
		"rate":       "input.*.sample_rate_hz",
		"start":      "input.*.start_utc",
		"swap":       "input.*.swap_iq",
		"order":      "decoder.bit_order",
		"unreadable": "decoder.unreadable",
		"format":     "output.format",
		"v":          "output.verbose",
	}
	flag.String("in", "", "input WAV file (mono = real audio, stereo = I/Q)")
	flag.String("device", "", "live: audio input to capture (part of its name, see -list-devices); use -device default for the system default input")
	flag.String("rate", "", "live / stdin: sample rate in Hz (default 48000)")
	stdin := flag.Bool("stdin", false, "read raw PCM from standard input (see -rate, -set input.stdin.channels, -set input.stdin.pcm_format)")
	flag.String("centre", "", "Hz: I/Q centre frequency, or for real audio the receiver LO; default: parsed from the file name")
	flag.String("start", "", `time of the first sample, UTC "yyyy-mm-dd hh:mm:ss"; default: from the file name (local time) or file time`)
	flag.String("swap", "", "true: stereo left = Q, right = I")
	flag.String("order", "", "bit order: auto, lsb or msb")
	flag.String("unreadable", "", "unreadable characters: nearest or star")
	flag.String("format", "", "output: text (time-stamped decoded text), messages or json")
	flag.Bool("v", false, "report carrier / framing / bit-order changes on stderr")
	chKHz := flag.Float64("channel", 0, "NAVTEX channel in kHz (518 or 490)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: navtex -in file.wav [-channel 518] [-centre Hz] [-set option=value ...] [-config file.json]")
		fmt.Fprintln(os.Stderr, "       navtex -device \"CABLE Output\" [-channel 518] [-centre Hz]      (live audio input)")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *listDev {
		devs, err := capture.List()
		if err != nil {
			fatal(err)
		}
		for _, d := range devs {
			fmt.Printf("%s\n", d.Name)
		}
		return
	}

	cfg := config.Default()
	if *cfgFile != "" {
		c, err := config.Load(*cfgFile)
		if err != nil {
			fatal(err)
		}
		cfg = c
	}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "device":
			cfg.Input.Source = "device"
		case "in":
			cfg.Input.Source = "file"
		}
	})
	if *stdin {
		cfg.Input.Source = "stdin"
	}
	group := map[string]string{"device": "device", "hpsdr": "hpsdr", "stdin": "stdin", "file": "file"}[cfg.Input.Source]
	flag.Visit(func(f *flag.Flag) {
		if path, ok := shortcuts[f.Name]; ok {
			if group == "file" && f.Name == "rate" {
				fatal(fmt.Errorf("-rate: a WAV file carries its own sample rate"))
			}
			if err := config.Set(&cfg, strings.Replace(path, "*", group, 1), f.Value.String()); err != nil {
				fatal(err)
			}
		}
		if f.Name == "channel" {
			if err := config.Set(&cfg, "channel.frequency_hz", fmt.Sprint(*chKHz*1e3)); err != nil {
				fatal(err)
			}
		}
	})
	flag.Visit(func(f *flag.Flag) { // -centre is an absolute frequency; the settings keep the LO's offset below the channel
		if f.Name != "centre" {
			return
		}
		var hz float64
		if _, err := fmt.Sscan(f.Value.String(), &hz); err != nil {
			fatal(fmt.Errorf("-centre %q: want a frequency in Hz", f.Value.String()))
		}
		cfg.SetCentre(hz)
	})
	for _, kv := range sets {
		path, val, ok := strings.Cut(kv, "=")
		if !ok {
			fatal(fmt.Errorf("-set %q: want option=value", kv))
		}
		if err := config.Set(&cfg, strings.TrimSpace(path), val); err != nil {
			fatal(err)
		}
	}

	switch {
	case *options:
		for _, f := range config.Fields(cfg) {
			extra := ""
			if len(f.Choices) > 0 {
				extra = " [" + strings.Join(f.Choices, "|") + "]"
			}
			if f.Unit != "" {
				extra += " (" + f.Unit + ")"
			}
			fmt.Printf("%-36s = %-10v %s%s\n", f.Path, f.Value, f.Doc, extra)
		}
		return
	case *dump:
		os.Stdout.Write(cfg.JSON())
		return
	}
	if err := cfg.Validate(); err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if cfg.Input.Source == "file" && cfg.Input.File.Path == "" {
		flag.Usage()
		os.Exit(2)
	}
	src, err := source.Open(ctx, cfg)
	if err != nil {
		fatal(err)
	}
	defer src.Close()
	if cfg.Output.Verbose || strings.HasPrefix(src.Info, "warning") {
		fmt.Fprintln(os.Stderr, src.Info)
	}
	start := src.Start

	enc := json.NewEncoder(os.Stdout)
	lw := &textout.LineWriter{W: os.Stdout}
	sum, err := pipeline.Run(ctx, src.Source, cfg, start, func(e pipeline.Event) {
		switch e.Kind {
		case pipeline.EvText:
			if cfg.Output.Format == "text" {
				lw.Write(e.Text, e.Wall)
			}
		case pipeline.EvMessage:
			m := e.Message
			switch cfg.Output.Format {
			case "json":
				enc.Encode(map[string]any{"time": e.Wall.UTC().Format(config.TimeFormat), "id": m.ID(), "station": m.Station,
					"subject": m.Subject, "serial": m.Serial, "complete": m.Complete, "body": m.Body})
			case "messages":
				note := ""
				if !m.Complete {
					note = "  [incomplete: no NNNN]"
				}
				fmt.Printf("=== %s  (station %s, subject %s, serial %s)  at %s UTC%s ===\n%s\n\n",
					m.ID(), m.Station, m.Subject, m.Serial, e.Wall.UTC().Format(config.TimeFormat), note, m.Body)
			}
		case pipeline.EvStatus:
			if cfg.Output.Verbose {
				fmt.Fprintf(os.Stderr, "%s [%s] %s\n", e.Wall.UTC().Format(config.TimeFormat), clock(e.Time), e.Text)
			}
		}
	})
	lw.Finish()
	if err != nil && ctx.Err() == nil {
		fatal(err)
	}
	if cfg.Output.Verbose {
		fmt.Fprintf(os.Stderr, "%.1f s, %d messages, %d locked symbols, blanked %.2f %%, carrier %+.1f Hz, bit order %v\n",
			sum.Seconds, sum.Messages, sum.SymbolsLocked, 100*sum.BlankedFrac, sum.FinalF0, sum.Order)
	}
	if sum.Messages == 0 {
		os.Exit(1)
	}
}

func clock(sec float64) string {
	return (time.Duration(sec * float64(time.Second))).Truncate(100 * time.Millisecond).String()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "navtex:", err)
	os.Exit(2)
}
