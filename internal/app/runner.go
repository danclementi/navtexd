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
	"navtex/internal/hpsdr"
	"navtex/internal/iq"
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
	Rcv  int       // the decoding channel (0 or 1)
}

// Hooks receive the runner's output. They are called from the decoding goroutine and must not block. Any may be nil.
type Hooks struct {
	Frame  func(Frame)
	Text   func(rcv int, text string, t time.Time)
	Line   func(rcv int, text string, quality float64, t time.Time) // a completed line of text
	Record func(store.Record)
	Log    func(store.SyslogEntry)
	State  func(State)
}

// State describes one decoding channel.
type State struct {
	Rcv     int       `json:"rcv"` // 0 = the first channel, 1 = the second
	Running bool      `json:"running"`
	Info    string    `json:"info"`  // what is being decoded
	Error   string    `json:"error"` // why it is not running, if it failed
	Since   time.Time `json:"since"`
	Live    bool      `json:"live"`
	Format  string    `json:"format"` // what the input delivers, e.g. "I/Q 48 kHz, LO 517.000 kHz" (empty while not running)
}

// chain is the running part of one decoding channel: its source, engine and the things the server asks of them.
type chain struct {
	idx   int
	scope atomic.Pointer[engine.Scope]
	ring  atomic.Pointer[clip.Ring]
	carr  atomic.Uint64 // last carrier position (audio Hz, float64 bits), for the clip description

	// guarded by Runner.mu
	state State
	cmds  chan func(*engine.Engine)
	stop  context.CancelFunc
	done  chan struct{}
}

// Runner owns the decoding channels (config.Channels of them; the second is optional), the configuration they share and
// what they produce.
type Runner struct {
	dir string
	st  *store.Store
	h   Hooks

	// Enrich, if set, completes a new record (station identification) before it is stored.
	Enrich func(*store.Record)
	// Radios, if set, is the list of radios used successfully (most recent first).
	Radios *hpsdr.MRU
	// Keep, if set, decides after identification whether a record is stored (selective logging).
	Keep func(store.Record) bool

	corr *autocorr.Corrector

	mu     sync.Mutex
	cfg    config.Config
	chains [config.Channels]*chain
	need   [config.Channels]bool // channels whose settings changed in a way that needs their restart
}

// New creates a runner; call Start to begin decoding.
func New(dataDir string, cfg config.Config, st *store.Store, h Hooks) *Runner {
	r := &Runner{dir: dataDir, cfg: cfg, st: st, h: h, corr: autocorr.New()}
	for i := range r.chains {
		r.chains[i] = &chain{idx: i}
	}
	return r
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

// ChanConfig returns the configuration of one decoding channel as a single-channel configuration (config.ForChannel).
func (r *Runner) ChanConfig(ch int) config.Config { return r.Config().ForChannel(ch) }

// State returns the state of one decoding channel.
func (r *Runner) State(ch int) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.chains[ch].state
	s.Rcv = ch
	return s
}

// log writes to the system log. Once the second channel is on, lines from a channel carry its label ("[ch1] ...").
func (r *Runner) log(level, text string) {
	e := r.st.Log(level, text)
	if r.h.Log != nil {
		r.h.Log(e)
	}
}

func (r *Runner) logc(ch int, level, text string) {
	if c := r.Config(); c.Second.Enabled {
		text = "[" + c.Label(ch) + "] " + text
	}
	r.log(level, text)
}

func (r *Runner) setState(ch int, s State) {
	s.Rcv = ch
	r.mu.Lock()
	r.chains[ch].state = s
	r.mu.Unlock()
	if r.h.State != nil {
		r.h.State(s)
	}
}

// Start begins decoding with the current configuration: the first channel, and the second one if it is enabled.
// Failures (no such file, no such audio device) are reported in the state and the system log, not returned, so that
// the server can still be used to fix the settings.
func (r *Runner) Start() {
	for i := range r.chains {
		if r.Config().ChannelEnabled(i) {
			r.startChain(i)
		}
	}
}

func (r *Runner) startChain(i int) {
	c := r.chains[i]
	r.mu.Lock()
	if c.stop != nil {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmds := make(chan func(*engine.Engine), 32)
	done := make(chan struct{})
	c.stop, c.cmds, c.done = cancel, cmds, done
	r.mu.Unlock()
	go func() {
		defer close(done)
		for {
			if !r.runGuarded(ctx, c, r.ChanConfig(i), cmds) {
				return
			}
			// the decoder hit an internal error: it was logged; start the channel again after a pause
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			r.logc(i, "warn", "restarting the channel after an internal error")
		}
	}()
}

// runGuarded runs the channel and turns a panic into a system-log entry instead of ending the program.
// It reports whether a panic happened.
func (r *Runner) runGuarded(ctx context.Context, c *chain, cfg config.Config, cmds chan func(*engine.Engine)) (panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			panicked = true
			stack := string(debug.Stack())
			if lines := strings.Split(stack, "\n"); len(lines) > 14 {
				stack = strings.Join(lines[5:14], " | ")
			}
			msg := fmt.Sprintf("internal error: %v (%s)", v, stack)
			r.logc(c.idx, "error", msg)
			r.setState(c.idx, State{Error: fmt.Sprintf("internal error: %v", v), Since: time.Now().UTC()})
		}
	}()
	r.run(ctx, c, cfg, cmds)
	return false
}

// Stop ends all decoding and waits for it to finish.
func (r *Runner) Stop() {
	for i := range r.chains {
		r.stopChain(i)
	}
}

func (r *Runner) stopChain(i int) {
	c := r.chains[i]
	r.mu.Lock()
	stop, done := c.stop, c.done
	c.stop, c.done, c.cmds = nil, nil, nil
	r.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// Restart applies the current configuration to the channels whose settings need it (see Modify), by stopping and
// starting them; with no such channel marked it restarts all. A channel that has been switched off stays stopped.
func (r *Runner) Restart() {
	r.mu.Lock()
	need := r.need
	r.need = [config.Channels]bool{}
	r.mu.Unlock()
	any := false
	for _, n := range need {
		any = any || n
	}
	for i := range r.chains {
		if any && !need[i] {
			continue
		}
		r.stopChain(i)
		if r.Config().ChannelEnabled(i) {
			r.startChain(i)
		} else {
			r.setState(i, State{})
		}
	}
}

// Do runs fn in the decoding goroutine of channel ch with its engine. It reports whether the channel is running.
func (r *Runner) Do(ch int, fn func(*engine.Engine)) bool {
	r.mu.Lock()
	c := r.chains[ch].cmds
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

// Modify changes the configuration under the lock, validates and saves it. restart says that at least one channel needs
// restarting to take effect (see config.RestartNeededFor); call Restart then, which restarts just those channels.
// Changes a running channel can take are applied to it at once.
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
	old := r.cfg
	var live [config.Channels]bool
	for i := range live {
		if config.RestartNeededFor(old, next, i) {
			restart = true
			r.need[i] = true
		} else if next.ChannelEnabled(i) && old.ForChannel(i) != next.ForChannel(i) {
			live[i] = true
		}
	}
	r.cfg = next
	r.mu.Unlock()
	for i, l := range live {
		if l { // whatever can change while running is applied to the engine (the latest configuration, whenever it runs)
			i := i
			r.Do(i, func(e *engine.Engine) { e.Reconfigure(r.ChanConfig(i)) })
		}
	}
	return restart, r.Save()
}

// Controls used by the header: they act on the running engine at once and are saved as settings.

func (r *Runner) Resync(ch int) bool { return r.Do(ch, func(e *engine.Engine) { e.Resync() }) }

func (r *Runner) SetPolarity(ch int, p sitor.Polarity) error {
	_, err := r.Modify(func(c *config.Config) error {
		c.EditChannel(ch, func(d *config.Config) { d.Decoder.Polarity = p })
		return nil
	})
	return err
}

func (r *Runner) SetUnshiftOnSpace(ch int, on bool) error {
	_, err := r.Modify(func(c *config.Config) error {
		c.EditChannel(ch, func(d *config.Config) { d.Decoder.UnshiftOnSpace = on })
		return nil
	})
	return err
}

func (r *Runner) SetAutotune(ch int, on bool) error {
	_, err := r.Modify(func(c *config.Config) error {
		c.EditChannel(ch, func(d *config.Config) { t := d.Tuning(); t.Autotune = on; d.SetTuning(t) })
		return nil
	})
	return err
}

// Scope returns the oscilloscope recorder of the running engine of channel ch (nil before the first start).
func (r *Runner) Scope(ch int) *engine.Scope { return r.chains[ch].scope.Load() }

// SetCarrier fixes the carrier of channel ch at an audio frequency (autotune off).
func (r *Runner) SetCarrier(ch int, hz float64) error {
	_, err := r.Modify(func(c *config.Config) error {
		c.EditChannel(ch, func(d *config.Config) {
			t := d.Tuning()
			t.Autotune, t.ManualCarrierHz = false, hz
			d.SetTuning(t)
		})
		return nil
	})
	return err
}

// SetComment changes the operational comment stored with new records (all channels).
func (r *Runner) SetComment(s string) error {
	_, err := r.Modify(func(c *config.Config) error { c.Operator.Comment = s; return nil })
	return err
}

func (r *Runner) run(ctx context.Context, c *chain, cfg config.Config, cmds chan func(*engine.Engine)) {
	log := func(level, text string) { r.logc(c.idx, level, text) }
	fail := func(err error) {
		log("error", err.Error())
		r.setState(c.idx, State{Error: err.Error(), Since: time.Now().UTC()})
	}
	src, err := source.Open(ctx, cfg, source.WithRadios(r.Radios))
	if err != nil {
		fail(err)
		return
	}
	defer src.Close()
	log("info", "start: "+src.Info)
	if src.Notices != nil {
		go func() {
			for {
				select {
				case m := <-src.Notices:
					log(m.Level, m.Text)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

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
		Status: func(text string) { log("info", text) },
		Text: func(text string, streamS float64) {
			if r.h.Text != nil {
				r.h.Text(c.idx, text, wallAt(streamS))
			}
		},
		Line: func(l segment.Line) {
			if r.h.Line != nil {
				r.h.Line(c.idx, l.Text, l.Quality, wallAt(l.StreamS))
			}
		},
		Record: func(sr segment.Record) {
			if r.storeRecord(c.idx, sr, wallAt) {
				stored++
			}
		},
		Meters: func(m engine.Meters) {
			if m.Carrier.Have {
				c.carr.Store(math.Float64bits(m.Carrier.AudioHz))
			}
			now := wallAt(m.StreamS)
			if c.idx == 0 && (m.Sync != engine.SyncLost || m.Quality > 0) { // (the quality history is the first channel's)
				r.st.AddQuality(now, m.Quality)
			}
			if time.Since(cpuAt) >= time.Second {
				cpuVal, cpuAt = cpu.Percent(), time.Now()
			}
			if r.h.Frame != nil {
				r.h.Frame(Frame{Meters: m, Time: now, CPU: cpuVal, Rcv: c.idx})
			}
		},
	})
	if err != nil {
		fail(err)
		return
	}
	c.scope.Store(eng.Scope())
	form := "audio"
	if src.Kind() != iq.Real {
		form = "I/Q"
	}
	r.setState(c.idx, State{Running: true, Info: src.Info, Since: time.Now().UTC(), Live: src.Live,
		Format: fmt.Sprintf("%s %.4g kHz, LO %.3f kHz", form, src.SampleRate()/1000, src.CentreHz()/1000)})

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
	if src.Live || cfg.Input.File.Realtime { // small blocks: about 50 ms, so meters and the display stay current
		block = max(256, int(src.SampleRate()/20))
	}
	buf := make([]complex64, block)
	var ring *clip.Ring
	if cl := cfg.Clips; cl.BeforeS+cl.AfterS > 0 {
		var got float64
		ring, got = clip.New(src.SampleRate(), src.Kind(), cl.BeforeS, cl.AfterS)
		if got < cl.BeforeS+cl.AfterS {
			log("warn", fmt.Sprintf("audio clips: memory limit, the clip will be %.0f s long instead of %.0f s", got, cl.BeforeS+cl.AfterS))
		}
		c.ring.Store(ring)
		defer c.ring.Store(nil)
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
	var wd *inputWatch
	if src.Live && cfg.Input.WatchdogS > 0 {
		wd = newInputWatch(time.Duration(cfg.Input.WatchdogS*float64(time.Second)), log)
		go wd.run(ctx)
	}
loop:
	for ctx.Err() == nil {
		drain()
		n, err := src.Read(buf)
		if n > 0 {
			if wd != nil {
				wd.block(buf[:n])
			}
			eng.Process(buf[:n])
			samples += int64(n)
			if ring != nil && ring.Write(buf[:n], inWall(samples)) {
				r.finishClip(c, ring, src)
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
		if cfg.Input.File.Realtime && !src.Live {
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
		r.finishClip(c, ring, src)
	}
	eng.Flush()
	sum := eng.Summary()
	log("info", fmt.Sprintf("stop: %.1f s, %d records stored, %d locked symbols, carrier %+.1f Hz, bit order %v", sum.Seconds, stored, sum.SymbolsLocked, sum.FinalF0, sum.Order))
	st := State{Info: src.Info, Since: time.Now().UTC()}
	switch {
	case endErr != nil:
		log("error", "read: "+endErr.Error())
		st.Error = "read: " + endErr.Error()
	case ctx.Err() == nil:
		st.Info = "end of input: " + src.Info
	}
	r.setState(c.idx, st)
}

// storeRecord completes, corrects, filters and stores one finished message or fragment, logging what happened. It
// reports whether the record was stored.
func (r *Runner) storeRecord(ch int, sr segment.Record, wallAt func(float64) time.Time) bool {
	full := r.Config()
	cfg := full.ForChannel(ch)
	rec := toStore(sr, cfg, wallAt)
	rec.Rcv = ch
	if r.Enrich != nil {
		r.Enrich(&rec)
	}
	notes := r.Correct(&rec)
	sc := cfg.Segment
	if keep, n := fragmentKept(rec, sc); !keep {
		r.log("info", fmt.Sprintf("DATABASE: fragment not stored (%d word(s) of %d+ letters, %d needed).", n, sc.FragWordLetters, sc.MinFragmentWords))
		return false
	}
	head := strings.TrimSpace(rec.B1 + rec.B2 + rec.Serial)
	if head == "" {
		head = "???"
	}
	if r.Keep != nil && !r.Keep(rec) {
		r.log("info", fmt.Sprintf("DATABASE: %s %s/%s not stored (selective logging).", rec.Kind, head, rec.Station))
		return false
	}
	saved, err := r.st.Add(rec)
	if err != nil {
		r.log("error", "cannot store record: "+err.Error())
		return false
	}
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
	return true
}

// toStore converts a segmenter record. cfg is the configuration of the channel that received it: the record carries the
// operator's comment and the channel's own comment.
func toStore(r segment.Record, cfg config.Config, wallAt func(float64) time.Time) store.Record {
	comment := cfg.Operator.Comment
	if c := strings.TrimSpace(cfg.Channel.Comment); c != "" {
		if comment != "" {
			comment += "; "
		}
		comment += c
	}
	rec := store.Record{
		Channel:  int(cfg.Channel.FrequencyHz/1000 + 0.5),
		Received: wallAt(r.StartS), End: wallAt(r.EndS),
		Kind: r.Kind.String(), B1: r.B1, B2: r.B2, Serial: r.Serial, Quality: store.F2(r.Quality), Comment: comment,
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
func (r *Runner) SaveClip(ch int) (float64, error) {
	ring := r.chains[ch].ring.Load()
	if ring == nil {
		return 0, fmt.Errorf("the clip function is off or nothing is running (options: clips)")
	}
	wait, err := ring.Request()
	if err != nil {
		return 0, err
	}
	r.logc(ch, "info", fmt.Sprintf("audio clip requested: the last %.0f s are kept, saving in %.0f s", r.Config().Clips.BeforeS, wait))
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

func (r *Runner) finishClip(c *chain, ring *clip.Ring, src *source.Opened) {
	frames, m := ring.Take()
	cfg := r.ChanConfig(c.idx)
	m.CentreHz, m.ChannelHz, m.Autotune, m.Software = src.CentreHz(), cfg.Channel.FrequencyHz, cfg.Autotune(), "navtexd"
	m.CarrierHz = math.Float64frombits(c.carr.Load())
	// the settings that matter for decoding; the network and personal options stay out
	var all map[string]json.RawMessage
	if json.Unmarshal(cfg.JSON(), &all) == nil {
		for _, k := range []string{"server", "operator", "logging", "output", "meters", "clips", "second"} {
			delete(all, k)
		}
		m.Settings, _ = json.Marshal(all)
	}
	dir := r.ClipDir()
	go func() {
		name, err := clip.Write(dir, frames, m)
		if err != nil {
			r.logc(c.idx, "error", "audio clip: "+err.Error())
			return
		}
		r.logc(c.idx, "info", fmt.Sprintf("audio clip saved: %s (%.0f s, %d kB)", name, m.Seconds, len(frames)*4/1024))
	}()
}
