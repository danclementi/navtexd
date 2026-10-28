package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"navtex/internal/hpsdr"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"navtex/internal/app"
	"navtex/internal/astro"
	"navtex/internal/capture"
	"navtex/internal/catalog"
	"navtex/internal/config"
	"navtex/internal/sitor"
	"navtex/internal/store"
)

//go:embed web
var webFS embed.FS

// Version is shown in the client.
const Version = "0.6"

// Server serves the API, the live event stream and the web client.
type Server struct {
	// Fallback lets the main listener move to a nearby free port when its address is in use. OnListen, if set, is told
	// the wanted and the actual address once the main listener is open.
	Fallback bool
	OnListen func(wanted, actual string)

	st     *store.Store
	cat    *catalog.Catalog
	run    *app.Runner
	hub    *Hub
	acc    *Access
	places *placer

	// Notify receives the console messages about connecting browsers (one line each); nil = print to standard output.
	Notify func(line string)

	sent        atomic.Int64 // bytes sent to clients
	netLast     atomic.Pointer[netMsg]
	netOnce     sync.Once
	qSmooth     [config.Channels]float64 // smoothed quality for the gauge
	qAt         [config.Channels]time.Time
	lPeak, lRMS [config.Channels]float64 // smoothed input level (dB) for the gauge
	lAt         [config.Channels]time.Time

	mu   sync.Mutex
	last [config.Channels]*meterMsg
	sun  *sunMsg
}

// New creates a server on top of the store. Call Attach with the runner before serving.
func New(st *store.Store) *Server { return &Server{st: st, hub: NewHub(), places: newPlacer()} }

// Attach connects the runner whose state and controls the server exposes.
func (s *Server) Attach(r *app.Runner, cat *catalog.Catalog) {
	s.run, s.cat = r, cat
	s.acc = NewAccess(func() config.Server { return r.Config().Server })
}

// Hub returns the event hub.
func (s *Server) Hub() *Hub { return s.hub }

// ---- messages sent to clients ----

type meterMsg struct {
	T       int64   `json:"t"` // wall clock, unix ms
	Peak    float64 `json:"peak"`
	RMS     float64 `json:"rms"`
	Clip    bool    `json:"clip"`
	Quality float64 `json:"q"`
	Sync    int     `json:"sync"` // 0 lost, 1 pre-sync, 2 sync
	Data    bool    `json:"data"`
	Have    bool    `json:"have"`          // carrier present
	Auto    bool    `json:"auto"`          // autotune
	Offset  float64 `json:"off"`           // carrier offset from the channel, Hz
	Audio   float64 `json:"audio"`         // carrier on the audio axis, Hz
	CPU     float64 `json:"cpu"`           // this program, percent of all cores
	Sys     float64 `json:"sys"`           // the whole machine, percent (-1 = not available)
	Cores   int     `json:"cores"`         // number of CPU cores
	Rcv     int     `json:"rcv,omitempty"` // decoding channel (0 = the first)
}

type spectrumMsg struct {
	Start   float64 `json:"start"`
	Step    float64 `json:"step"`
	Carrier float64 `json:"carrier"`
	Filt    []int16 `json:"filt"`
	Raw     []int16 `json:"raw,omitempty"`
	Chan    []int16 `json:"chan,omitempty"` // after the channel filter; -3000 = no data
	FS      float64 `json:"fs"`             // level of a full-scale tone, dB (same units as the spectrum)
	Rcv     int     `json:"rcv,omitempty"`  // decoding channel (0 = the first)
}

type sunMsg struct {
	Rise string `json:"rise"` // UTC HH:MM
	Set  string `json:"set"`
	Up   bool   `json:"up"` // the sun is above the horizon now
}

type controlsMsg struct {
	Channel  int     `json:"channel"` // kHz
	Polarity string  `json:"polarity"`
	UoS      bool    `json:"uos"`
	Autotune bool    `json:"autotune"`
	Carrier  float64 `json:"carrier"`
	Comment  string  `json:"comment"`
	Source   string  `json:"source"`
	LineQ    bool    `json:"lineq"`  // show the quality figure on text lines
	Device   string  `json:"device"` // administrators only (empty for viewers)
	Rcv      int     `json:"rcv"`    // decoding channel these controls belong to (0 = the first)
	Enabled  bool    `json:"enabled"`
	Name     string  `json:"name"`  // the channel's name option, else empty
	Label    string  `json:"label"` // name, or "ch1" / "ch2"
	ChanNote string  `json:"note"`  // the channel's comment option
}

type recordMsg struct {
	Action    string  `json:"action,omitempty"` // new, update, delete (events only)
	ID        int64   `json:"id"`
	Version   int     `json:"v"`
	Channel   int     `json:"ch"`
	Received  int64   `json:"rx"`
	End       int64   `json:"end"`
	Kind      string  `json:"kind"`
	B1        string  `json:"b1"`
	B2        string  `json:"b2"`
	Serial    string  `json:"ser"`
	NavArea   int     `json:"area"`
	StationID int     `json:"sid"`
	Station   string  `json:"st"`
	Quality   float64 `json:"q"`
	Km        float64 `json:"km,omitempty"`
	Bearing   float64 `json:"brg,omitempty"`
	Comment   string  `json:"cmt"`
	Manual    bool    `json:"man"`
	Method    string  `json:"im"`
	Lines     int     `json:"lines"`
	Rcv       int     `json:"rcv,omitempty"` // decoding channel that received it (0 = the first)
}

// brief is the list / event form of a record; distance and bearing only when withDist.
func brief(r store.Record, withDist bool) recordMsg {
	m := recordMsg{ID: r.ID, Version: r.Version, Channel: r.Channel, Received: r.Received.UnixMilli(), End: r.End.UnixMilli(), Kind: r.Kind,
		B1: r.B1, B2: r.B2, Serial: r.Serial, NavArea: r.NavArea, StationID: r.StationID, Station: r.Station, Quality: float64(r.Quality),
		Comment: r.Comment, Manual: r.Manual, Method: r.IdentMethod, Lines: len(r.Lines), Rcv: r.Rcv}
	if withDist {
		m.Km, m.Bearing = r.DistanceKm, r.BearingDeg
	}
	return m
}

func (s *Server) controls(rcv int, admin bool) controlsMsg {
	full := s.run.Config()
	c := full.ForChannel(rcv)
	pol, _ := c.Decoder.Polarity.MarshalText()
	m := controlsMsg{Channel: int(c.Channel.FrequencyHz/1000 + 0.5), Polarity: string(pol), UoS: c.Decoder.UnshiftOnSpace, Autotune: c.Autotune(),
		Carrier: c.ManualCarrierHz(), Comment: c.Operator.Comment, Source: c.Input.Source, LineQ: c.Logging.LineQuality,
		Rcv: rcv, Enabled: full.ChannelEnabled(rcv), Name: strings.TrimSpace(c.Channel.Name), Label: full.Label(rcv), ChanNote: c.Channel.Comment}
	if admin {
		m.Device = c.Input.Device.Name
	}
	return m
}

// chanHello is what a new client is told about one decoding channel.
type chanHello struct {
	Controls controlsMsg `json:"controls"`
	Run      app.State   `json:"run"`
	Meters   *meterMsg   `json:"meters,omitempty"`
	Text     []textChunk `json:"text"`
	Lines    []lineMsg   `json:"lines"`
}

type hello struct {
	Role      string      `json:"role"`
	NeedToken bool        `json:"need_token"`
	Version   string      `json:"version"`
	Now       int64       `json:"now"`
	Run       app.State   `json:"run"`      // the first channel (see Chans)
	Controls  controlsMsg `json:"controls"` // "
	Meters    *meterMsg   `json:"meters,omitempty"`
	Sun       *sunMsg     `json:"sun,omitempty"`
	Text      []textChunk `json:"text"`
	Lines     []lineMsg   `json:"lines"`
	Chans     []chanHello `json:"chans"` // every decoding channel, the first one included
	Clients   int         `json:"clients"`
	Net       *netMsg     `json:"net,omitempty"`
	Records   int         `json:"records"`
	Loc       bool        `json:"loc"` // this client may see location-based information (distances, bearings, sun times, ephemeris)
}

func (s *Server) hello(id Identity) hello {
	s.mu.Lock()
	last, sun := s.last, s.sun
	s.mu.Unlock()
	if id.Role != Admin && !s.run.Config().Operator.Public {
		sun = nil
	}
	chans := make([]chanHello, config.Channels)
	for i := range chans {
		chans[i] = chanHello{Controls: s.controls(i, id.Role == Admin), Run: s.run.State(i), Meters: last[i], Text: s.hub.Recent(i), Lines: s.hub.Lines(i)}
	}
	return hello{Role: id.Role.String(), NeedToken: id.NeedToken, Version: Version, Now: time.Now().UnixMilli(), Run: chans[0].Run,
		Controls: chans[0].Controls, Meters: last[0], Sun: sun, Text: chans[0].Text, Lines: chans[0].Lines, Chans: chans,
		Clients: s.hub.Clients(), Net: s.netLast.Load(), Records: s.st.Count(), Loc: s.withDist(id)}
}

// Hooks returns the runner hooks that feed the clients. Pass them to app.New.
func (s *Server) Hooks() app.Hooks {
	return app.Hooks{
		Frame: func(f app.Frame) {
			q := s.smoothQuality(f.Rcv, f.Quality, f.Time)
			pk, rm := s.smoothLevel(f.Rcv, f.Level.Peak, f.Level.RMS, f.Time)
			m := &meterMsg{T: f.Time.UnixMilli(), Peak: round(pk, 4), RMS: round(rm, 4), Clip: f.Level.Clip, Quality: round(q, 1),
				Sync: int(f.Sync), Data: f.Data, Have: f.Carrier.Have, Auto: f.Carrier.Autotune, Offset: round(f.Carrier.OffsetHz, 1),
				Audio: round(f.Carrier.AudioHz, 1), CPU: round(f.CPU, 1), Sys: round(f.Sys, 1), Cores: runtime.NumCPU(), Rcv: f.Rcv}
			s.mu.Lock()
			s.last[f.Rcv] = m
			s.mu.Unlock()
			s.hub.Publish("meters", m, true, false)
			if sp := f.Spectrum; sp != nil {
				var raw []int16
				if s.run.Config().Meters.SpectrumRaw {
					raw = dB(sp.Raw)
				}
				var ch []int16
				if sp.Channel != nil {
					ch = dB(sp.Channel)
				}
				s.hub.Publish("spectrum", spectrumMsg{sp.StartHz, sp.StepHz, sp.CarrierHz, dB(sp.Filtered), raw, ch, round(sp.FullScaleDB, 1), f.Rcv}, true, false)
			}
		},
		Text: func(rcv int, text string, t time.Time) {
			s.hub.AddText(rcv, t, text)
			s.hub.Publish("text", textChunk{t.UnixMilli(), text, rcv}, false, false)
		},
		Line: func(rcv int, text string, q float64, t time.Time) {
			l := lineMsg{t.UnixMilli(), round(q, 0), text, rcv}
			s.hub.AddLine(l)
			s.hub.Publish("line", l, false, false)
		},
		Record: func(r store.Record) {
			m := brief(r, false)
			m.Action = "new"
			s.hub.ClearLines(r.Rcv)
			s.hub.Publish("record", m, false, false)
		},
		Log:   func(e store.SyslogEntry) { s.hub.Publish("log", e, false, true) },
		State: func(st app.State) { s.hub.Publish("run", st, false, false) },
	}
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}

func dB(v []float32) []int16 {
	out := make([]int16, len(v))
	for i, x := range v {
		out[i] = int16(math.Round(float64(x) * 10)) // tenths of a dB
	}
	return out
}

// ---- sun ----

func (s *Server) updateSun() {
	c := s.run.Config().Operator
	var m *sunMsg
	if c.LatDeg != 0 || c.LonDeg != 0 {
		now := time.Now().UTC()
		if r, st, ok := astro.SunTimes(now, c.LatDeg, c.LonDeg); ok {
			m = &sunMsg{Rise: r.Format("15:04"), Set: st.Format("15:04"), Up: now.After(r) && now.Before(st)}
		} else {
			m = &sunMsg{Rise: "--:--", Set: "--:--", Up: astro.Elevation(now, c.LatDeg, c.LonDeg) > 0}
		}
	}
	s.mu.Lock()
	changed := (m == nil) != (s.sun == nil) || (m != nil && *m != *s.sun)
	s.sun = m
	s.mu.Unlock()
	if changed && m != nil {
		s.hub.Publish("sun", m, false, !s.run.Config().Operator.Public)
	}
}

// ---- serving ----

// Serve serves until ctx ends. listen and tunnel are listen addresses (either may be empty).
func (s *Server) Serve(ctx context.Context, listen, tunnel string) error {
	var srvs []*http.Server
	errc := make(chan error, 2)
	start := func(addr string, isTunnel bool) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil && !isTunnel && s.Fallback {
			ln, err = listenNear(addr)
		}
		if err != nil {
			return err
		}
		if !isTunnel && s.OnListen != nil {
			s.OnListen(addr, ln.Addr().String())
		}
		srv := &http.Server{Handler: s.handler(isTunnel), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		srvs = append(srvs, srv)
		go func() { errc <- srv.Serve(ln) }()
		return nil
	}
	if listen != "" {
		if err := start(listen, false); err != nil {
			return err
		}
	}
	if tunnel != "" {
		if err := start(tunnel, true); err != nil {
			for _, sv := range srvs {
				sv.Close()
			}
			return err
		}
	}
	if len(srvs) == 0 {
		return errors.New("nothing to listen on")
	}
	s.updateSun()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			s.updateSun()
		case err := <-errc:
			for _, sv := range srvs {
				sv.Close()
			}
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			s.Bye("The server has been shut down.")
			for _, sv := range srvs {
				sv.Close()
			}
			return nil
		}
	}
}

// listenNear is used when the wanted address is already taken: it tries the next nine ports on the same host and, as a
// last resort, lets the system pick a free port.
func listenNear(addr string) (net.Listener, error) {
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("cannot listen on %s", addr)
	}
	for p := port + 1; p <= port+9 && p < 65536; p++ {
		if ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p))); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, "0"))
}

// Bye tells every connected browser that the server is closing (they show a message and keep trying to reconnect), and
// gives the event streams a moment to deliver it.
func (s *Server) Bye(msg string) {
	if s.hub.Clients() == 0 {
		return
	}
	s.hub.Publish("bye", map[string]string{"msg": msg}, false, false)
	time.Sleep(400 * time.Millisecond)
}

func (s *Server) handler(tunnel bool) http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	if d := os.Getenv("NAVTEX_WEB_DIR"); d != "" { // development: serve the client files from disk
		sub = os.DirFS(d)
	}
	files := http.FileServer(http.FS(sub))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /api/state", s.viewer(tunnel, func(w http.ResponseWriter, r *http.Request, id Identity) { writeJSON(w, 200, s.hello(id)) }))
	mux.HandleFunc("GET /api/events", s.events(tunnel))
	s.recordRoutes(mux, tunnel)
	s.extraRoutes(mux, tunnel)
	mux.HandleFunc("GET /api/settings", s.admin(tunnel, s.getSettings))
	mux.HandleFunc("PUT /api/settings", s.admin(tunnel, s.putSettings))
	mux.HandleFunc("POST /api/settings/reset-channel", s.admin(tunnel, s.resetChannel))
	mux.HandleFunc("POST /api/settings/restore-channel", s.admin(tunnel, s.restoreChannel))
	mux.HandleFunc("GET /api/audio-devices", s.admin(tunnel, s.devices))
	mux.HandleFunc("GET /api/radios", s.admin(tunnel, s.radios))
	mux.HandleFunc("GET /api/radio/state", s.admin(tunnel, s.radioState))
	mux.HandleFunc("POST /api/radio/release", s.admin(tunnel, s.radioRelease))
	mux.HandleFunc("GET /api/input", s.admin(tunnel, s.input))
	mux.HandleFunc("POST /api/control/{name}", s.admin(tunnel, s.control))
	mux.HandleFunc("GET /api/syslog", s.admin(tunnel, s.syslog))
	mux.HandleFunc("POST /api/login", s.login(tunnel))
	s.netOnce.Do(func() { go s.netLoop() })
	return s.secure(mux)
}

// countWriter counts the bytes sent to clients (all responses, including the event streams).
type countWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c countWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n.Add(int64(n))
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (flush, deadlines).
func (c countWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (s *Server) secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = countWriter{w, &s.sent}
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		// a bug in one handler must not drop the connection (the browser would only say "Failed to fetch"):
		// answer 500 with the reason and put it in the system log
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				if s.run != nil {
					s.run.Log("error", fmt.Sprintf("%s %s: internal error: %v", r.Method, r.URL.Path, v))
				}
				fail(w, 500, fmt.Errorf("internal error: %v", v))
			}
		}()
		h.ServeHTTP(w, r)
	})
}

type handler func(w http.ResponseWriter, r *http.Request, id Identity)

func (s *Server) viewer(tunnel bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h(w, r, s.acc.Classify(r, tunnel)) }
}

func (s *Server) admin(tunnel bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := s.acc.Classify(r, tunnel)
		if id.Role != Admin {
			msg := "settings can only be changed from the local network"
			if id.NeedToken {
				msg = "access token needed"
			}
			writeJSON(w, http.StatusForbidden, map[string]any{"error": msg, "need_token": id.NeedToken})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if err := CheckWrite(r); err != nil {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
				return
			}
		}
		h(w, r, id)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

func readBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

// ---- event stream ----

func (s *Server) events(tunnel bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := s.acc.Classify(r, tunnel)
		rc := http.NewResponseController(w)
		gap := time.Duration(0)
		if id.Role != Admin {
			gap = 300 * time.Millisecond // viewers (often over the internet) get fewer spectrum frames
		}
		c := s.hub.add(id.Role, gap)
		end := s.noteConn(r, id, tunnel) // prints "connected" and later "disconnected"
		defer end()
		defer s.hub.remove(c)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		send := func(name string, data []byte) bool {
			rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data); err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		hb, _ := json.Marshal(s.hello(id))
		if !send("hello", hb) {
			return
		}
		ka := time.NewTicker(15 * time.Second)
		defer ka.Stop()
		for {
			select {
			case ev, ok := <-c.ch:
				if !ok || !send(ev.name, ev.data) {
					return
				}
			case <-ka.C:
				rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	}
}

// ---- settings and controls (administrators) ----

type fieldMsg struct {
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`
	Value   any      `json:"value"`
	Doc     string   `json:"doc"`
	Unit    string   `json:"unit,omitempty"`
	Choices []string `json:"choices,omitempty"`
	Apply   string   `json:"apply"` // live | channel | server
}

func applyKind(path string) string {
	switch {
	case strings.HasPrefix(path, "server."):
		return "server"
	case config.IsLive(path):
		return "live"
	}
	return "channel"
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, id Identity) {
	var out []fieldMsg
	for _, f := range config.Fields(s.run.Config()) {
		out = append(out, fieldMsg{f.Path, f.Kind, f.Value, f.Doc, f.Unit, f.Choices, applyKind(f.Path)})
	}
	hist := make([]int, config.Channels)
	for i := range hist {
		hist[i] = s.run.HistoryCount(i)
	}
	writeJSON(w, 200, map[string]any{"fields": out, "history": hist})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Changes map[string]json.RawMessage `json:"changes"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if len(body.Changes) == 0 {
		fail(w, 400, errors.New("no changes"))
		return
	}
	before := s.run.Config()
	oldServer := before.Server
	oldPublic := before.Operator.Public
	restart, err := s.run.Modify(func(c *config.Config) error {
		for path, raw := range body.Changes {
			if err := config.Set(c, path, string(raw)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(w, 400, err)
		return
	}
	names := make([]string, 0, len(body.Changes))
	touched := map[int]bool{}
	for k := range body.Changes {
		names = append(names, k)
		if m := extraPath.FindStringSubmatch(k); m != nil {
			touched[int(m[1][0]-'0')+1] = true
		} else if chanGroup.MatchString(k) {
			touched[0] = true
		}
	}
	for ch := range touched {
		s.run.PushHistory(ch, before, false)
	}
	s.run.Log("info", "settings changed: "+strings.Join(names, ", "))
	if restart {
		s.run.Restart()
	}
	s.updateSun()
	if pub := s.run.Config().Operator.Public; pub != oldPublic {
		s.hub.Publish("location", map[string]bool{"public": pub}, false, false) // viewers reconnect and are told afresh
	}
	s.publishControls()
	writeJSON(w, 200, map[string]any{"ok": true, "restarted": restart, "server_restart": s.run.Config().Server != oldServer})
}

// resetChannel restores the default settings of one decoding channel (body {"ch": 0..3}); name, comment and the on/off
// switch stay.
func (s *Server) resetChannel(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Ch int `json:"ch"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if body.Ch < 0 || body.Ch >= config.Channels {
		fail(w, 400, errors.New("no such channel"))
		return
	}
	before := s.run.Config()
	restart, err := s.run.Modify(func(c *config.Config) error { c.ResetChannel(body.Ch); return nil })
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.run.PushHistory(body.Ch, before, true)
	s.run.Log("info", fmt.Sprintf("%sdefault settings restored", s.chTag(body.Ch)))
	if restart {
		s.run.Restart()
	}
	s.publishControls()
	writeJSON(w, 200, map[string]any{"ok": true, "restarted": restart})
}

var (
	extraPath = regexp.MustCompile(`^extra\.(\d)\.`)
	chanGroup = regexp.MustCompile(`^(channel|input|ddc|receiver|decoder|segment)\.`)
)

// restoreChannel puts a channel's settings back to what they were before the latest bout of edits (body {"ch": 0..3}).
func (s *Server) restoreChannel(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Ch int `json:"ch"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if body.Ch < 0 || body.Ch >= config.Channels {
		fail(w, 400, errors.New("no such channel"))
		return
	}
	restart, err := s.run.RestorePrevious(body.Ch)
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("%sprevious settings restored", s.chTag(body.Ch)))
	if restart {
		s.run.Restart()
	}
	s.publishControls()
	writeJSON(w, 200, map[string]any{"ok": true, "restarted": restart})
}

// publishControls tells every client the new control settings (the device name is left out; administrators read it
// from the settings).
func (s *Server) publishControls() {
	for i := 0; i < config.Channels; i++ {
		s.hub.Publish("controls", s.controls(i, false), false, false)
	}
}

// chTag prefixes a system-log line about channel ch once more than one channel is on.
func (s *Server) chTag(ch int) string {
	if c := s.run.Config(); c.ActiveChannels() > 1 {
		return "[" + c.Label(ch) + "] "
	}
	return ""
}

// rcvOf reads the decoding channel a request is about (query "ch", 0 to Channels-1; default 0).
func rcvOf(r *http.Request) (int, error) {
	v := r.URL.Query().Get("ch")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n >= config.Channels {
		return 0, fmt.Errorf("ch must be 0 to %d, not %q", config.Channels-1, v)
	}
	return n, nil
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request, id Identity) {
	devs, err := capture.List()
	if err != nil { // no audio system to ask (e.g. a server without sound): an empty list
		devs = nil
	}
	type d struct {
		Name string `json:"name"`
		Note string `json:"note,omitempty"`
	}
	out := []d{}
	for _, x := range devs {
		out = append(out, d{x.Name, x.Note})
	}
	writeJSON(w, 200, out)
}

// input says where the samples come from now (administrators only; the broadcast controls leave it out).
func (s *Server) input(w http.ResponseWriter, r *http.Request, id Identity) {
	ch, err := rcvOf(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	c := s.run.ChanConfig(ch).Input
	m := map[string]string{"source": c.Source, "device": c.Device.Name, "radio": c.HPSDR.Address}
	if c.Source == "sdrconnect" {
		m["radio"] = strings.TrimSpace(c.SDRconnect.Address)
		m["device"] = c.SDRconnect.Device
	}
	if c.Source == "spyserver" {
		m["radio"] = strings.TrimSpace(c.SpyServer.Address)
	}
	if c.SharedFrom > 0 {
		m["shared"] = s.run.Config().Label(c.SharedFrom - 1)
	}
	writeJSON(w, 200, m)
}

// radios lists the radios that have been used successfully, most recently used first (administrators only: the addresses
// are part of the receiver's setup).
func (s *Server) radios(w http.ResponseWriter, r *http.Request, id Identity) {
	type d struct {
		Addr  string `json:"addr"`
		Name  string `json:"name,omitempty"`
		Label string `json:"label"`
	}
	out := []d{}
	for _, k := range s.run.Radios.List() {
		out = append(out, d{k.Addr, k.Name, k.Label()})
	}
	writeJSON(w, 200, out)
}

// radioState reports whether the radio at ?addr= is streaming to some host, and whether that is this program.
func (s *Server) radioState(w http.ResponseWriter, r *http.Request, id Identity) {
	st, err := hpsdr.Probe(r.Context(), strings.TrimSpace(r.URL.Query().Get("addr")), 0)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, st)
}

// radioRelease sends the stop command to a radio that reports being in use by another host. The page asks the
// administrator twice before calling it.
func (s *Server) radioRelease(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Addr string `json:"addr"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	addr := strings.TrimSpace(body.Addr)
	st, err := hpsdr.Release(r.Context(), addr, 0)
	if err != nil {
		s.run.Log("warn", fmt.Sprintf("radio %s: release refused or failed: %v", addr, err))
		fail(w, 400, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("radio %s: stop command sent by an administrator (%s)", addr, map[bool]string{true: "it still reports being in use", false: "it is free now"}[st.Busy]))
	writeJSON(w, 200, st)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if r.ContentLength != 0 {
		if err := readBody(r, &body); err != nil {
			fail(w, 400, err)
			return
		}
	}
	asBool := func() (bool, error) { var b bool; err := json.Unmarshal(body.Value, &b); return b, err }
	asFloat := func() (float64, error) { var f float64; err := json.Unmarshal(body.Value, &f); return f, err }
	asString := func() (string, error) { var t string; err := json.Unmarshal(body.Value, &t); return t, err }
	var err error
	name := r.PathValue("name")
	ch, cerr := rcvOf(r)
	if cerr != nil {
		fail(w, 400, cerr)
		return
	}
	switch name {
	case "resync":
		if !s.run.Resync(ch) {
			err = errors.New("the channel is not running")
		}
	case "polarity":
		var t string
		var p sitor.Polarity
		if t, err = asString(); err == nil {
			if err = p.UnmarshalText([]byte(t)); err == nil {
				err = s.run.SetPolarity(ch, p)
			}
		}
	case "uos":
		var b bool
		if b, err = asBool(); err == nil {
			err = s.run.SetUnshiftOnSpace(ch, b)
		}
	case "autotune":
		var b bool
		if b, err = asBool(); err == nil {
			err = s.run.SetAutotune(ch, b)
		}
	case "carrier":
		var f float64
		if f, err = asFloat(); err == nil {
			if lo, hi := s.run.CarrierRange(ch); f < lo || f > hi {
				err = fmt.Errorf("carrier must be between %.0f and %.0f Hz for this input", lo, hi)
			} else if win, ok := s.run.WindowCenter(ch); ok && math.Abs(f-win) > carrierReachHz {
				err = fmt.Errorf("the carrier can be at most %.0f Hz from the center of the receiver window (%.0f Hz); move the LO (lo_below_hz) to bring the signal closer", carrierReachHz, win)
			} else {
				err = s.run.SetCarrier(ch, f)
			}
		}
	case "view", "viewreset": // center the displayed spectrum slice elsewhere, or back on the receiver window
		var f float64
		reset := name == "viewreset"
		if !reset {
			f, err = asFloat()
		}
		if err == nil {
			lo, hi := s.run.CarrierRange(ch)
			err = s.run.MoveView(ch, math.Max(lo, math.Min(hi, f)), reset)
		}
	case "comment":
		var t string
		if t, err = asString(); err == nil {
			err = s.run.SetComment(strings.TrimSpace(t))
		}
	case "frequency":
		var f float64
		if f, err = asFloat(); err == nil {
			if f != 518 && f != 490 && f != 486 && f != 424 {
				err = errors.New("frequency must be 518, 490, 486 or 424 (kHz)")
			} else {
				var restart bool
				restart, err = s.run.Modify(func(c *config.Config) error {
					c.EditChannel(ch, func(d *config.Config) { d.Channel.FrequencyHz = f * 1000 })
					return nil
				})
				if err == nil && restart {
					s.run.Log("info", fmt.Sprintf("%schannel changed to %.0f kHz", s.chTag(ch), f))
					s.run.Restart()
				}
			}
		}
	case "device":
		var t string
		if t, err = asString(); err == nil {
			var restart bool
			restart, err = s.run.Modify(func(c *config.Config) error {
				c.EditChannel(ch, func(d *config.Config) { d.Input.Source, d.Input.Device.Name = "device", t })
				return nil
			})
			if err == nil && restart {
				s.run.Log("info", s.chTag(ch)+"audio input changed to "+t)
				s.run.Restart()
			}
		}
	case "radio": // take I/Q from a radio: its address, or empty to search the network
		var t string
		if t, err = asString(); err == nil {
			t = strings.TrimSpace(t)
			var restart bool
			restart, err = s.run.Modify(func(c *config.Config) error {
				c.EditChannel(ch, func(d *config.Config) { d.Input.Source, d.Input.HPSDR.Address = "hpsdr", t })
				return nil
			})
			if err == nil && restart {
				if t == "" {
					t = "(search the network)"
				}
				s.run.Log("info", s.chTag(ch)+"input changed to radio "+t)
				s.run.Restart()
			}
		}
	default:
		fail(w, 404, fmt.Errorf("no control %q", name))
		return
	}
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.publishControls()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) syslog(w http.ResponseWriter, r *http.Request, id Identity) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > store.SyslogKeep {
		n = 500
	}
	writeJSON(w, 200, s.st.Syslog(n))
}

func (s *Server) login(tunnel bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := CheckWrite(r); err != nil {
			fail(w, 403, err)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, 400, err)
			return
		}
		tok := s.run.Config().Server.Token
		if tok == "" || !tokenOK(&http.Request{Header: http.Header{"X-Navtex-Token": {body.Token}}}, tok) {
			fail(w, 403, errors.New("wrong token"))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "navtex_token", Value: body.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
			Secure: r.TLS != nil, MaxAge: 90 * 24 * 3600})
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// netMsg is the network load shown in the header.
type netMsg struct {
	Bps   int `json:"bps"`   // bytes per second sent to all clients, averaged over the last interval
	Users int `json:"users"` // connected live clients
}

// netLoop publishes the network load every two seconds.
func (s *Server) netLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	last, at := s.sent.Load(), time.Now()
	for range t.C {
		now, nt := s.sent.Load(), time.Now()
		m := netMsg{Bps: int(float64(now-last) / nt.Sub(at).Seconds()), Users: s.hub.Clients()}
		last, at = now, nt
		s.netLast.Store(&m)
		s.hub.Publish("net", m, true, false)
	}
}

// smoothQuality is the first-order low-pass of the quality gauge (time constant meters.quality_display_tau_s).
func (s *Server) smoothQuality(ch int, q float64, t time.Time) float64 {
	tau := s.run.Config().Meters.QualityTauS
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := t.Sub(s.qAt[ch]).Seconds()
	if tau <= 0 || s.qAt[ch].IsZero() || dt <= 0 || dt > 10*tau+5 {
		s.qSmooth[ch] = q
	} else {
		s.qSmooth[ch] += (q - s.qSmooth[ch]) * (1 - math.Exp(-dt/tau))
	}
	s.qAt[ch] = t
	return s.qSmooth[ch]
}

// smoothLevel is the first-order low-pass of the input level gauge, applied to the level in dB
// (time constant meters.level_display_tau_s). Peak and RMS are linear amplitudes in and out.
func (s *Server) smoothLevel(ch int, peak, rms float64, t time.Time) (float64, float64) {
	tau := s.run.Config().Meters.LevelTauS
	toDB := func(v float64) float64 {
		if v < 1e-5 {
			return -100
		}
		return 20 * math.Log10(v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := t.Sub(s.lAt[ch]).Seconds()
	p, r := toDB(peak), toDB(rms)
	if tau <= 0 || s.lAt[ch].IsZero() || dt <= 0 || dt > 10*tau+5 {
		s.lPeak[ch], s.lRMS[ch] = p, r
	} else {
		a := 1 - math.Exp(-dt/tau)
		s.lPeak[ch] += (p - s.lPeak[ch]) * a
		s.lRMS[ch] += (r - s.lRMS[ch]) * a
	}
	s.lAt[ch] = t
	if peak <= 0 {
		return 0, 0
	}
	return math.Pow(10, s.lPeak[ch]/20), math.Pow(10, s.lRMS[ch]/20)
}

// carrierReachHz: how far from the center of the receiver window a manual carrier may be set (the receiver's baseband
// is about +-1800 Hz wide).
const carrierReachHz = 1800.0
