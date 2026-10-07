// Package app runs the decoder for the daemon: it owns the sample source and the engine, stores finished records, keeps
// the system log and quality history, applies live controls and settings changes, and reports everything to hooks (the
// web server, the console).
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"navtex/internal/autocorr"
	"navtex/internal/clip"
	"navtex/internal/config"
	"navtex/internal/engine"
	"navtex/internal/segment"
	"navtex/internal/sitor"
	"navtex/internal/source"
	"navtex/internal/store"
	"navtex/internal/sysinfo"
)

// SettingsFile is the name of the saved configuration inside the data directory.
const SettingsFile = "settings.json"

// Frame is one periodic report for displays.
type Frame struct {
	engine.Meters
	Time time.Time // wall-clock (UTC) time of the frame
	CPU  float64   // this process's CPU use, percent of the machine
}

// Hooks receive the runner's output. They are called from the decoding goroutine and must not block. Any may be nil.
type Hooks struct {
	Frame  func(Frame)
	Text   func(text string, t time.Time)
	Line   func(text string, quality float64, t time.Time) // a completed line of text
	Record func(store.Record)
	Log    func(store.SyslogEntry)
	State  func(State)
}

// State describes the decoding channel.
type State struct {
	Running bool      `json:"running"`
	Info    string    `json:"info"`  // what is being decoded
	Error   string    `json:"error"` // why it is not running, if it failed
	Since   time.Time `json:"since"`
	Live    bool      `json:"live"`
}

// Runner is the decoding channel.
type Runner struct {
	dir string
	st  *store.Store
	h   Hooks

	// Enrich, if set, completes a new record (station identification) before it is stored.
	Enrich func(*store.Record)
	// Keep, if set, decides after identification whether a record is stored (selective logging).
	Keep func(store.Record) bool

	scope atomic.Pointer[engine.Scope]
	ring  atomic.Pointer[clip.Ring]
	carr  atomic.Uint64 // last carrier position (audio Hz, float64 bits), for the clip description
	corr  *autocorr.Corrector

	mu    sync.Mutex
	cfg   config.Config
	state State
	cmds  chan func(*engine.Engine)
	stop  context.CancelFunc
	done  chan struct{}
}

// New creates a runner; call Start to begin decoding.
func New(dataDir string, cfg config.Config, st *store.Store, h Hooks) *Runner {
	return &Runner{dir: dataDir, cfg: cfg, st: st, h: h, corr: autocorr.New()}
}

// AutocorrConfig converts the options for the autocorrection package.
func AutocorrConfig(a config.Autocorrect) autocorr.Config {
	return autocorr.Config{Enabled: a.Enabled, MinLen: a.MinLen, MaxLen: a.MaxLen, MaxMarkedPct: a.MaxUnreadable, MaxCandidates: a.MaxCandidates}
}

// Corrector returns the vocabulary, brought up to date with the user's extra words file.
func (r *Runner) Corrector() (*autocorr.Corrector, autocorr.Config) {
	a := r.Config().Autocorrect
	path := a.WordsFile
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(r.dir, path)
	}
	if changed, err := r.corr.Refresh(path); err != nil {
		r.log("warn", "vocabulary file: "+err.Error())
	} else if changed && path != "" {
		r.log("info", fmt.Sprintf("autocorrect: vocabulary now has %d words", r.corr.Size()))
	}
	return r.corr, AutocorrConfig(a)
}

// Correct fills in the autocorrected text of a record (the raw lines are not touched). It returns what was corrected.
func (r *Runner) Correct(rec *store.Record) []string {
	c, cfg := r.Corrector()
	body, notes := c.Body(rec.EffectiveLines(), cfg) // the operator's revisions count, the raw lines are not touched
	rec.Corrected = body
	return notes
}

// Config returns a copy of the current configuration.
func (r *Runner) Config() config.Config { r.mu.Lock(); defer r.mu.Unlock(); return r.cfg }

// State returns the current state.
func (r *Runner) State() State { r.mu.Lock(); defer r.mu.Unlock(); return r.state }

func (r *Runner) log(level, text string) {
	e := r.st.Log(level, text)
	if r.h.Log != nil {
		r.h.Log(e)
	}
}

func (r *Runner) setState(s State) {
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
	if r.h.State != nil {
		r.h.State(s)
	}
}

// Start begins decoding with the current configuration. Failures (no such file, no such audio device) are reported in
// the state and the system log, not returned, so that the server can still be used to fix the settings.
func (r *Runner) Start() {
	r.mu.Lock()
	if r.stop != nil {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmds := make(chan func(*engine.Engine), 32)
	done := make(chan struct{})
	r.stop, r.cmds, r.done = cancel, cmds, done
	r.mu.Unlock()
	go func() {
		defer close(done)
		for {
			if !r.runGuarded(ctx, r.Config(), cmds) {
				return
			}
			// the decoder hit an internal error: it was logged; start the channel again after a pause
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			r.log("warn", "restarting the channel after an internal error")
		}
	}()
}

// runGuarded runs the channel and turns a panic into a system-log entry instead of ending the program.
// It reports whether a panic happened.
func (r *Runner) runGuarded(ctx context.Context, cfg config.Config, cmds chan func(*engine.Engine)) (panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			panicked = true
			stack := string(debug.Stack())
			if lines := strings.Split(stack, "\n"); len(lines) > 14 {
				stack = strings.Join(lines[5:14], " | ")
			}
			msg := fmt.Sprintf("internal error: %v (%s)", v, stack)
			r.log("error", msg)
			r.setState(State{Error: fmt.Sprintf("internal error: %v", v), Since: time.Now().UTC()})
		}
	}()
	r.run(ctx, cfg, cmds)
	return false
}

// Stop ends decoding and waits for it to finish.
func (r *Runner) Stop() {
	r.mu.Lock()
	stop, done := r.stop, r.done
	r.stop, r.done, r.cmds = nil, nil, nil
	r.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// Restart applies the current configuration by stopping and starting the channel.
func (r *Runner) Restart() { r.Stop(); r.Start() }

// Do runs fn in the decoding goroutine with the engine. It reports whether the channel is running.
func (r *Runner) Do(fn func(*engine.Engine)) bool {
	r.mu.Lock()
	c := r.cmds
	r.mu.Unlock()
	if c == nil {
		return false
	}
	select {
	case c <- fn:
		return true
	default:
		return false
	}
}

// Save writes the configuration to the settings file in the data directory.
func (r *Runner) Save() error {
	cfg := r.Config()
	return cfg.Save(filepath.Join(r.dir, SettingsFile))
}

// Modify changes the configuration under the lock, validates and saves it. restart says the change needs the channel
// restarted to take effect (anything other than the operator information and the server settings).
func (r *Runner) Modify(change func(*config.Config) error) (restart bool, err error) {
	r.mu.Lock()
	next := r.cfg
	if err = change(&next); err == nil {
		err = next.Validate()
	}
	if err != nil {
		r.mu.Unlock()
		return false, err
	}
	restart = config.RestartNeeded(r.cfg, next)
	changed := r.cfg != next
	r.cfg = next
	r.mu.Unlock()
	if changed && !restart {
		// whatever can change while running is applied to the engine (the latest configuration, whenever it runs)
		r.Do(func(e *engine.Engine) { e.Reconfigure(r.Config()) })
	}
	return restart, r.Save()
}

// Controls used by the header: they act on the running engine at once and are saved as settings.

func (r *Runner) Resync() bool { return r.Do(func(e *engine.Engine) { e.Resync() }) }

func (r *Runner) SetPolarity(p sitor.Polarity) error {
	if _, err := r.Modify(func(c *config.Config) error { c.Decoder.Polarity = p; return nil }); err != nil {
		return err
	}
	return nil
}

func (r *Runner) SetUnshiftOnSpace(on bool) error {
	if _, err := r.Modify(func(c *config.Config) error { c.Decoder.UnshiftOnSpace = on; return nil }); err != nil {
		return err
	}
	return nil
}

func (r *Runner) SetAutotune(on bool) error {
	if _, err := r.Modify(func(c *config.Config) error { c.Channel.Autotune = on; return nil }); err != nil {
		return err
	}
	return nil
}

// Scope returns the oscilloscope recorder of the running engine (nil before the first start).
func (r *Runner) Scope() *engine.Scope { return r.scope.Load() }

// SetCarrier fixes the carrier at an audio frequency (autotune off).
func (r *Runner) SetCarrier(hz float64) error {
	if _, err := r.Modify(func(c *config.Config) error { c.Channel.Autotune, c.Channel.ManualCarrierHz = false, hz; return nil }); err != nil {
		return err
	}
	return nil
}

// SetComment changes the operational comment stored with new records.
func (r *Runner) SetComment(s string) error {
	_, err := r.Modify(func(c *config.Config) error { c.Operator.Comment = s; return nil })
	return err
}

func (r *Runner) run(ctx context.Context, cfg config.Config, cmds chan func(*engine.Engine)) {
	fail := func(err error) {
		r.log("error", err.Error())
		r.setState(State{Error: err.Error(), Since: time.Now().UTC()})
	}
	src, err := source.Open(ctx, cfg)
	if err != nil {
		fail(err)
		return
	}
	defer src.Close()
	r.log("info", "start: "+src.Info)

	var eng *engine.Engine
	wallAt := func(streamS float64) time.Time {
		if src.Start.IsZero() {
			return time.Now().UTC().Add(-time.Duration((eng.Now() - streamS) * float64(time.Second)))
		}
		return src.Start.UTC().Add(time.Duration(streamS * float64(time.Second)))
	}
	var cpu sysinfo.CPUMeter
	var cpuVal float64
	var cpuAt time.Time
	stored := 0
	eng, err = engine.New(src.SampleRate(), src.CentreHz(), src.Kind(), cfg, engine.Handlers{
		Status: func(text string) { r.log("info", text) },
		Text: func(text string, streamS float64) {
			if r.h.Text != nil {
				r.h.Text(text, wallAt(streamS))
			}
		},
		Line: func(l segment.Line) {
			if r.h.Line != nil {
				r.h.Line(l.Text, l.Quality, wallAt(l.StreamS))
			}
		},
		Record: func(sr segment.Record) {
			rec := toStore(sr, r.Config(), wallAt)
			if r.Enrich != nil {
				r.Enrich(&rec)
			}
			notes := r.Correct(&rec)
			head := strings.TrimSpace(rec.B1 + rec.B2 + rec.Serial)
			if head == "" {
				head = "???"
			}
			if r.Keep != nil && !r.Keep(rec) {
				r.log("info", fmt.Sprintf("DATABASE: %s %s/%s not stored (selective logging).", rec.Kind, head, rec.Station))
				return
			}
			saved, err := r.st.Add(rec)
			if err != nil {
				r.log("error", "cannot store record: "+err.Error())
				return
			}
			stored++
			switch {
			case saved.Kind == "fragment":
				r.log("info", fmt.Sprintf("DATABASE: fragment stored (%d lines).", len(saved.Lines)))
			case saved.B1 == "":
				r.log("info", fmt.Sprintf("DATABASE: no valid ID-char for %s/%d, %s stored.", head, saved.NavArea, saved.Kind))
			case saved.StationID == 0:
				r.log("info", fmt.Sprintf("DATABASE: %s %s/UNID stored, station could not be identified.", saved.Kind, head))
			default:
				r.log("info", fmt.Sprintf("DATABASE: %s %s/%s stored (%s).", saved.Kind, head, saved.Station, saved.IdentMethod))
			}
			for i, n := range notes {
				if i == 12 {
					r.log("info", fmt.Sprintf("autocorrect: ... and %d more words", len(notes)-i))
					break
				}
				r.log("info", "autocorrect: "+n)
			}
			if r.h.Record != nil {
				r.h.Record(saved)
			}
		},
		Meters: func(m engine.Meters) {
			if m.Carrier.Have {
				r.carr.Store(math.Float64bits(m.Carrier.AudioHz))
			}
			now := wallAt(m.StreamS)
			if m.Sync != engine.SyncLost || m.Quality > 0 {
				r.st.AddQuality(now, m.Quality)
			}
			if time.Since(cpuAt) >= time.Second {
				cpuVal, cpuAt = cpu.Percent(), time.Now()
			}
			if r.h.Frame != nil {
				r.h.Frame(Frame{Meters: m, Time: now, CPU: cpuVal})
			}
		},
	})
	if err != nil {
		fail(err)
		return
	}
	r.scope.Store(eng.Scope())
	r.setState(State{Running: true, Info: src.Info, Since: time.Now().UTC(), Live: src.Live})

	drain := func() {
		for {
			select {
			case fn := <-cmds:
				fn(eng)
			default:
				return
			}
		}
	}
	block := cfg.Input.BlockSize
	if src.Live {
		block = max(256, int(src.SampleRate()/20))
	} else if cfg.Input.Realtime {
		block = max(256, int(src.SampleRate()/20))
	}
	buf := make([]complex64, block)
	var ring *clip.Ring
	if c := cfg.Clips; c.BeforeS+c.AfterS > 0 {
		var got float64
		ring, got = clip.New(src.SampleRate(), src.Kind(), c.BeforeS, c.AfterS)
		if got < c.BeforeS+c.AfterS {
			r.log("warn", fmt.Sprintf("audio clips: memory limit, the clip will be %.0f s long instead of %.0f s", got, c.BeforeS+c.AfterS))
		}
		r.ring.Store(ring)
		defer r.ring.Store(nil)
	}
	inWall := func(frames int64) time.Time {
		if src.Live || src.Start.IsZero() {
			return time.Now().UTC()
		}
		return src.Start.UTC().Add(time.Duration(float64(frames) / src.SampleRate() * float64(time.Second)))
	}
	t0 := time.Now()
	var samples int64
	endErr := error(nil)
loop:
	for ctx.Err() == nil {
		drain()
		n, err := src.Read(buf)
		if n > 0 {
			eng.Process(buf[:n])
			samples += int64(n)
			if ring != nil && ring.Write(buf[:n], inWall(samples)) {
				r.finishClip(ring, src)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() == nil {
				endErr = err
			}
			break
		}
		if cfg.Input.Realtime && !src.Live {
			target := t0.Add(time.Duration(float64(samples) / src.SampleRate() * float64(time.Second)))
			for {
				d := time.Until(target)
				if d <= 0 {
					break
				}
				if ctx.Err() != nil {
					break loop
				}
				drain()
				time.Sleep(min(d, 20*time.Millisecond))
			}
		}
	}
	if ring != nil && ring.Pending() { // the input ended before the post-roll was over: save what there is
		r.finishClip(ring, src)
	}
	eng.Flush()
	sum := eng.Summary()
	r.log("info", fmt.Sprintf("stop: %.1f s, %d records stored, %d locked symbols, carrier %+.1f Hz, bit order %v", sum.Seconds, stored, sum.SymbolsLocked, sum.FinalF0, sum.Order))
	st := State{Info: src.Info, Since: time.Now().UTC()}
	switch {
	case endErr != nil:
		r.log("error", "read: "+endErr.Error())
		st.Error = "read: " + endErr.Error()
	case ctx.Err() == nil:
		st.Info = "end of input: " + src.Info
	}
	r.setState(st)
}

// ToStore converts a segmenter record.
func toStore(r segment.Record, cfg config.Config, wallAt func(float64) time.Time) store.Record {
	rec := store.Record{
		Channel:  int(cfg.Channel.FrequencyHz/1000 + 0.5),
		Received: wallAt(r.StartS), End: wallAt(r.EndS),
		Kind: r.Kind.String(), B1: r.B1, B2: r.B2, Serial: r.Serial, Quality: store.F2(r.Quality), Comment: cfg.Operator.Comment,
	}
	for _, l := range r.Lines {
		rec.Lines = append(rec.Lines, store.Line{Time: wallAt(l.StreamS), Quality: store.F2(l.Quality), Text: l.Text})
	}
	return rec
}

// Log writes a line to the system log (and the hooks).
func (r *Runner) Log(level, text string) { r.log(level, text) }

// SaveClip asks for the last seconds of input (plus the configured time after now) to be saved as a WAV file. It
// returns the number of seconds until the clip is complete.
func (r *Runner) SaveClip() (float64, error) {
	ring := r.ring.Load()
	if ring == nil {
		return 0, fmt.Errorf("the clip function is off or nothing is running (options: clips)")
	}
	wait, err := ring.Request()
	if err != nil {
		return 0, err
	}
	r.log("info", fmt.Sprintf("audio clip requested: the last %.0f s are kept, saving in %.0f s", r.Config().Clips.BeforeS, wait))
	return wait, nil
}

// ClipDir is the folder of saved clips.
func (r *Runner) ClipDir() string {
	d := r.Config().Clips.Dir
	if d == "" {
		d = "clips"
	}
	if !filepath.IsAbs(d) {
		d = filepath.Join(r.dir, d)
	}
	return d
}

func (r *Runner) finishClip(ring *clip.Ring, src *source.Opened) {
	frames, m := ring.Take()
	cfg := r.Config()
	m.CentreHz, m.ChannelHz, m.Autotune, m.Software = src.CentreHz(), cfg.Channel.FrequencyHz, cfg.Channel.Autotune, "navtexd"
	m.CarrierHz = math.Float64frombits(r.carr.Load())
	// the settings that matter for decoding; the network and personal options stay out
	var all map[string]json.RawMessage
	if json.Unmarshal(cfg.JSON(), &all) == nil {
		for _, k := range []string{"server", "operator", "logging", "output", "meters", "clips"} {
			delete(all, k)
		}
		m.Settings, _ = json.Marshal(all)
	}
	dir := r.ClipDir()
	go func() {
		name, err := clip.Write(dir, frames, m)
		if err != nil {
			r.log("error", "audio clip: "+err.Error())
			return
		}
		r.log("info", fmt.Sprintf("audio clip saved: %s (%.0f s, %d kB)", name, m.Seconds, len(frames)*4/1024))
	}()
}
