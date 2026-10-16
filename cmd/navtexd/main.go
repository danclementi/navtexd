// Command navtexd is the NAVTEX decoder daemon: it decodes a live audio input (or a recording), splits the text into
// records (messages, incomplete messages, fragments) and keeps them, with a system log and a quality history, in a data
// directory. This is milestone 1 (headless); the network server and web client build on it.
//
// Options are the same as for the navtex command (-config, -set, -dump-config, -options, -in, -device, -stdin, ...).
// Extra flags: -data DIR (default navtex-data), -list, -show ID. While running, single-line commands may be typed on
// standard input (not with -stdin): resync, autotune on|off, carrier HZ, polarity auto|normal|inverted, uos on|off.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"navtex/internal/app"
	"navtex/internal/capture"
	"navtex/internal/catalog"
	"navtex/internal/config"
	"navtex/internal/dirlock"
	"navtex/internal/hpsdr"
	"navtex/internal/server"
	"navtex/internal/sitor"
	"navtex/internal/store"
)

type setFlags []string

func (s *setFlags) String() string     { return strings.Join(*s, ",") }
func (s *setFlags) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var (
		cfgFile = flag.String("config", "", "JSON configuration file (see -dump-config)")
		dump    = flag.Bool("dump-config", false, "print the effective configuration as JSON and exit")
		options = flag.Bool("options", false, "list every option and exit")
		listDev = flag.Bool("list-devices", false, "list the audio inputs and exit")
		dataDir = flag.String("data", defaultDataDir(), "data directory (message log, settings, station table). Default: navtex-data next to the program. Only one copy of the program can use a folder")
		list    = flag.Bool("list", false, "list the stored records and exit")
		show    = flag.Int64("show", 0, "print one stored record (by id) and exit")
		stdin   = flag.Bool("stdin", false, "read raw PCM from standard input")
		verbose = flag.Bool("v", false, "print status changes and a level/quality line every 5 s")
		sets    setFlags
	)
	flag.Var(&sets, "set", "override any option: -set decoder.unreadable=star (repeatable)")
	// Command-line shortcuts for options. A name with "*" in its path is filled in with the group of the selected
	// source (device, hpsdr, stdin or file).
	shortcuts := map[string]string{"in": "input.file.path", "device": "input.device.name", "radio": "input.hpsdr.address", "rate": "input.*.sample_rate_hz",
		"centre": "input.*.centre_hz", "start": "input.*.start_utc", "carrier": "input.*.manual_carrier_hz", "channel": "channel.frequency_hz",
		"listen": "server.listen", "tunnel": "server.tunnel_listen", "realtime": "input.file.realtime"}
	flag.String("in", "", "input WAV file")
	flag.String("device", "", "audio input to capture (part of its name; see -list-devices)")
	flag.String("radio", "", "take I/Q from an OpenHPSDR (Metis) radio at this address; use auto to search the network")
	flag.String("rate", "", "sample rate in Hz of the selected live input (a radio offers 48000, 96000, 192000, 384000; a WAV file has its own)")
	flag.String("centre", "", "Hz: receiver LO (real audio) or I/Q centre of the selected input; live default: channel - 1000 Hz (radio: channel - 3000 Hz)")
	flag.String("carrier", "", "Hz: fix the carrier at this position in the selected input's audio / baseband and turn autotune off")
	flag.String("start", "", "UTC time of the first sample (yyyy-mm-dd hh:mm:ss) for a piped recording")
	flag.String("channel", "", "channel frequency in Hz (518000, 490000, 424000)")
	flag.String("listen", "", "web server address, e.g. 127.0.0.1:8080, :8080 for all interfaces, off for none")
	flag.String("tunnel", "", "extra view-only address for a reverse tunnel such as cloudflared, e.g. 127.0.0.1:8081")
	flag.Bool("realtime", false, "play a WAV file back at its real speed (to test the web interface)")
	flag.Parse()
	explicitFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	if *listDev {
		devs, err := capture.List()
		if err != nil {
			fatal(err)
		}
		for _, d := range devs {
			fmt.Println(d.Name)
		}
		return
	}
	if *list || *show != 0 {
		listRecords(*dataDir, *show)
		return
	}

	cfg := config.Default()
	settings := filepath.Join(*dataDir, app.SettingsFile)
	if *cfgFile == "" {
		if _, err := os.Stat(settings); err == nil {
			*cfgFile = settings // settings saved from the web interface
		}
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fatal(err)
	}
	if *cfgFile != "" {
		c, err := config.Load(*cfgFile)
		if err != nil {
			fatal(err)
		}
		cfg = c
	}
	base := cfg // the configuration before the command line, for remembering the input choice below
	// first the flags that choose the source, then the ones that set its options
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "device":
			cfg.Input.Source = "device"
		case "in":
			cfg.Input.Source = "file" // a file on the command line wins over the default live input
		case "radio":
			cfg.Input.Source = "hpsdr"
		}
	})
	if *stdin {
		cfg.Input.Source = "stdin"
	}
	group := map[string]string{"device": "device", "hpsdr": "hpsdr", "stdin": "stdin", "file": "file"}[cfg.Input.Source]
	flag.Visit(func(f *flag.Flag) {
		path, ok := shortcuts[f.Name]
		if !ok {
			return
		}
		v := f.Value.String()
		if f.Name == "radio" && strings.EqualFold(v, "auto") {
			v = ""
		}
		if strings.Contains(path, "*") {
			if group == "file" && f.Name == "rate" {
				fatal(fmt.Errorf("-rate: a WAV file carries its own sample rate"))
			}
			path = strings.Replace(path, "*", group, 1)
		}
		if err := config.Set(&cfg, path, v); err != nil {
			fatal(err)
		}
		if f.Name == "carrier" {
			if err := config.Set(&cfg, "input."+group+".autotune", "false"); err != nil {
				fatal(err)
			}
		}
	})
	rememberInput(base, cfg, explicitFlags, settings)
	for _, kv := range sets {
		path, val, ok := strings.Cut(kv, "=")
		if !ok {
			fatal(fmt.Errorf("-set %q: want option=value", kv))
		}
		if err := config.Set(&cfg, strings.TrimSpace(path), val); err != nil {
			fatal(err)
		}
	}
	if *options {
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
	}
	if *dump {
		os.Stdout.Write(cfg.JSON())
		return
	}
	if err := cfg.Validate(); err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lock, err := dirlock.Acquire(*dataDir)
	if err != nil {
		if errors.Is(err, dirlock.ErrBusy) {
			fatal(fmt.Errorf("the data folder %s is already in use by another copy of navtexd.\nTo run a second copy, unpack the program into another folder and start it from there (or choose a folder with -data)", *dataDir))
		}
		fatal(err)
	}
	defer lock.Release()
	if abs, err := filepath.Abs(*dataDir); err == nil {
		fmt.Printf("data folder: %s\n", abs)
	} else {
		fmt.Printf("data folder: %s\n", *dataDir)
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		fatal(err)
	}
	defer st.Close()

	cat, err := catalog.Open(*dataDir, st)
	if err != nil {
		fatal(err)
	}
	srv := server.New(st)
	web := srv.Hooks()
	var lastPrint float64
	hooks := app.Hooks{
		Text:  web.Text,
		Line:  web.Line,
		State: web.State,
		Record: func(rec store.Record) {
			web.Record(rec)
			fmt.Printf("%s  stored #%d %-10s %s%s%s  %d lines  quality %.0f\n", rec.Received.Format(config.TimeFormat), rec.ID, rec.Kind,
				rec.B1, rec.B2, rec.Serial, len(rec.Lines), float64(rec.Quality))
		},
		Log: func(e store.SyslogEntry) {
			web.Log(e)
			if *verbose || e.Level != "info" {
				prefix := ""
				if e.Level != "info" {
					prefix = strings.ToUpper(e.Level) + " "
				}
				fmt.Printf("%s  %s%s\n", e.Time.Format(config.TimeFormat), prefix, e.Text)
			}
		},
		Frame: func(f app.Frame) {
			web.Frame(f)
			if *verbose && f.StreamS-lastPrint >= 5 {
				lastPrint = f.StreamS
				clip := "     "
				if f.Level.Clip {
					clip = " CLIP"
				}
				fmt.Printf("%s  level %3.0f%%%s  quality %3.0f  sync %-8v data %-5v carrier %7.1f Hz  cpu %.1f%%\n", f.Time.Format(config.TimeFormat),
					100*f.Level.Peak, clip, f.Quality, f.Sync, f.Data, f.Carrier.AudioHz, f.CPU)
			}
		},
	}
	run := app.New(*dataDir, cfg, st, hooks)
	cat.Position = func() (float64, float64, bool) {
		o := run.Config().Operator
		return o.LatDeg, o.LonDeg, o.LatDeg != 0 || o.LonDeg != 0
	}
	run.Enrich = cat.Enrich
	run.Radios = hpsdr.OpenMRU(filepath.Join(*dataDir, "radios.json"))
	run.Keep = func(r store.Record) bool { return cat.Logged(r.StationID) }
	srv.Attach(run, cat)
	run.Start()
	defer run.Stop()

	served := make(chan struct{}) // closed when the web server has stopped (its clients have been told)
	if cfg.Server.Listen == "" || cfg.Server.Listen == "off" {
		close(served)
	}
	if cfg.Server.Listen != "" && cfg.Server.Listen != "off" {
		go func() {
			srv.Fallback = !explicitFlags["listen"]
			srv.OnListen = func(wanted, actual string) {
				if wanted != actual {
					fmt.Printf("%s is in use (another navtexd running?); using %s instead\n", wanted, actual)
				}
				url := "http://" + displayAddr(actual) + "/"
				fmt.Printf("web interface on %s\n", url)
				if cfg.Server.OpenBrowser {
					openBrowser(url)
				}
			}
			if cfg.Server.TunnelListen != "" {
				fmt.Printf("tunnel (view only) on %s\n", cfg.Server.TunnelListen)
			}
			defer close(served)
			if err := srv.Serve(ctx, cfg.Server.Listen, cfg.Server.TunnelListen); err != nil {
				fmt.Fprintln(os.Stderr, "navtexd: web server:", err)
				stop()
			}
		}()
	}

	cmds := make(chan string, 16)
	if cfg.Input.Source != "stdin" {
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				cmds <- strings.TrimSpace(sc.Text())
			}
		}()
	}
	// Run until interrupted; a recording that ends leaves the server up so its results can be looked at.
	for {
		select {
		case c := <-cmds:
			apply(run, c)
		case <-ctx.Done():
			select { // let the web server tell its browsers that it is closing
			case <-served:
			case <-time.After(2 * time.Second):
			}
			return
		}
	}
}

// defaultDataDir is the folder "navtex-data" next to the program, so that a copy unpacked into its own folder keeps its
// data there. A program started with "go run" (in a temporary folder) uses the current folder instead.
func defaultDataDir() string {
	exe, err := os.Executable()
	if err != nil || strings.Contains(exe, "go-build") || strings.HasPrefix(exe, os.TempDir()) {
		return "navtex-data"
	}
	return filepath.Join(filepath.Dir(exe), "navtex-data")
}

func displayAddr(a string) string {
	if host, port, err := net.SplitHostPort(a); err == nil {
		if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
			return "localhost:" + port
		}
	}
	return a
}

func apply(r *app.Runner, line string) {
	f := strings.Fields(line)
	ch := 0 // a leading "ch2", "ch3" ... addresses another channel
	if len(f) > 0 && len(f[0]) == 3 && strings.EqualFold(f[0][:2], "ch") && f[0][2] >= '1' && int(f[0][2]-'1') < config.Channels {
		ch = int(f[0][2] - '1')
		f = f[1:]
	}
	if len(f) == 0 {
		return
	}
	arg := ""
	if len(f) > 1 {
		arg = strings.ToLower(f[1])
	}
	switch strings.ToLower(f[0]) {
	case "resync":
		r.Resync(ch)
	case "autotune":
		r.SetAutotune(ch, arg != "off")
	case "uos":
		r.SetUnshiftOnSpace(ch, arg != "off")
	case "carrier":
		if hz, err := strconv.ParseFloat(arg, 64); err == nil {
			r.SetCarrier(ch, hz)
		} else {
			fmt.Fprintln(os.Stderr, "carrier HZ: not a number")
		}
	case "polarity":
		var p sitor.Polarity
		if err := p.UnmarshalText([]byte(arg)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		r.SetPolarity(ch, p)
	default:
		fmt.Fprintln(os.Stderr, "commands: [ch1|ch2] resync | autotune on|off | carrier HZ | polarity auto|normal|inverted | uos on|off")
	}
}

func listRecords(dir string, id int64) {
	st, err := store.Open(dir)
	if err != nil {
		fatal(err)
	}
	defer st.Close()
	if id != 0 {
		r, ok := st.Get(id)
		if !ok {
			fatal(fmt.Errorf("no record %d", id))
		}
		fmt.Printf("#%d %s %d kHz  %s .. %s UTC  %s%s%s  quality %.0f  station %s\n", r.ID, r.Kind, r.Channel, r.Received.Format(config.TimeFormat),
			r.End.Format("15:04:05"), r.B1, r.B2, r.Serial, r.Quality, r.Station)
		for _, l := range r.Lines {
			fmt.Printf("%s %3.0f> %s\n", l.Time.Format(config.TimeFormat), l.Quality, l.Text)
		}
		return
	}
	recs, total := st.Query(store.Filter{})
	for _, r := range recs {
		first := ""
		if len(r.Lines) > 1 {
			first = r.Lines[1].Text
		}
		if len(first) > 50 {
			first = first[:50]
		}
		fmt.Printf("#%-4d %s  %-10s %s%s%-2s  %2d lines  q %3.0f  %s\n", r.ID, r.Received.Format(config.TimeFormat), r.Kind, r.B1, r.B2, r.Serial, len(r.Lines), r.Quality, first)
	}
	fmt.Printf("%d records\n", total)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "navtexd:", err)
	os.Exit(2)
}

// rememberInput saves the radio or audio device given on the command line as the program's own setting, so that the next
// start without the option uses it too (the Options page shows the value but cannot "change" it to save it). Only that
// choice is kept; everything else given on the command line is for this run only, and so is a file or stdin input.
func rememberInput(base, cfg config.Config, explicit map[string]bool, settings string) {
	if cfg.Input.Source == "file" || cfg.Input.Source == "stdin" {
		return
	}
	keep := base
	switch {
	case explicit["radio"] && cfg.Input.Source == "hpsdr":
		keep.Input.Source, keep.Input.HPSDR.Address = "hpsdr", cfg.Input.HPSDR.Address
	case explicit["device"] && cfg.Input.Source == "device":
		keep.Input.Source, keep.Input.Device.Name = "device", cfg.Input.Device.Name
	default:
		return
	}
	if keep.Input == base.Input {
		if _, err := os.Stat(settings); err == nil {
			return // already saved that way
		}
	}
	if err := keep.Save(settings); err != nil {
		fmt.Fprintln(os.Stderr, "navtexd: could not remember the input choice:", err)
		return
	}
	fmt.Println("remembered the input choice in", settings, "(change it later on the Options page)")
}
