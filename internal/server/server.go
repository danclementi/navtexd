package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"net/http"
	"os"
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
const Version = "0.2"

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
	qSmooth     float64 // smoothed quality for the gauge
	qAt         time.Time
	lPeak, lRMS float64 // smoothed input level (dB) for the gauge
	lAt         time.Time

	mu   sync.Mutex
	last *meterMsg
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
	Have    bool    `json:"have"`  // carrier present
	Auto    bool    `json:"auto"`  // autotune
	Offset  float64 `json:"off"`   // carrier offset from the channel, Hz
	Audio   float64 `json:"audio"` // carrier on the audio axis, Hz
	CPU     float64 `json:"cpu"`
}

type spectrumMsg struct {
	Start   float64 `json:"start"`
	Step    float64 `json:"step"`
	Carrier float64 `json:"carrier"`
	Filt    []int16 `json:"filt"`
	Raw     []int16 `json:"raw,omitempty"`
	FS      float64 `json:"fs"` // level of a full-scale tone, dB (same units as the spectrum)
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
	LineQ    bool    `json:"lineq"`            // show the quality figure on text lines
	Device   string  `json:"device,omitempty"` // administrators only
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
}

// brief is the list / event form of a record; distance and bearing only when withDist.
func brief(r store.Record, withDist bool) recordMsg {
	m := recordMsg{ID: r.ID, Version: r.Version, Channel: r.Channel, Received: r.Received.UnixMilli(), End: r.End.UnixMilli(), Kind: r.Kind,
		B1: r.B1, B2: r.B2, Serial: r.Serial, NavArea: r.NavArea, StationID: r.StationID, Station: r.Station, Quality: float64(r.Quality),
		Comment: r.Comment, Manual: r.Manual, Method: r.IdentMethod, Lines: len(r.Lines)}
	if withDist {
		m.Km, m.Bearing = r.DistanceKm, r.BearingDeg
	}
	return m
}

func (s *Server) controls(admin bool) controlsMsg {
	c := s.run.Config()
	pol, _ := c.Decoder.Polarity.MarshalText()
	m := controlsMsg{Channel: int(c.Channel.FrequencyHz/1000 + 0.5), Polarity: string(pol), UoS: c.Decoder.UnshiftOnSpace, Autotune: c.Channel.Autotune,
		Carrier: c.Channel.ManualCarrierHz, Comment: c.Operator.Comment, Source: c.Input.Source, LineQ: c.Logging.LineQuality}
	if admin {
		m.Device = c.Input.Device
	}
	return m
}

type hello struct {
	Role      string      `json:"role"`
	NeedToken bool        `json:"need_token"`
	Version   string      `json:"version"`
	Now       int64       `json:"now"`
	Run       app.State   `json:"run"`
	Controls  controlsMsg `json:"controls"`
	Meters    *meterMsg   `json:"meters,omitempty"`
	Sun       *sunMsg     `json:"sun,omitempty"`
	Text      []textChunk `json:"text"`
	Lines     []lineMsg   `json:"lines"`
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
	return hello{Role: id.Role.String(), NeedToken: id.NeedToken, Version: Version, Now: time.Now().UnixMilli(), Run: s.run.State(),
		Controls: s.controls(id.Role == Admin), Meters: last, Sun: sun, Text: s.hub.Recent(), Lines: s.hub.Lines(), Clients: s.hub.Clients(), Net: s.netLast.Load(), Records: s.st.Count(), Loc: s.withDist(id)}
}

// Hooks returns the runner hooks that feed the clients. Pass them to app.New.
func (s *Server) Hooks() app.Hooks {
	return app.Hooks{
		Frame: func(f app.Frame) {
			q := s.smoothQuality(f.Quality, f.Time)
			pk, rm := s.smoothLevel(f.Level.Peak, f.Level.RMS, f.Time)
			m := &meterMsg{T: f.Time.UnixMilli(), Peak: round(pk, 4), RMS: round(rm, 4), Clip: f.Level.Clip, Quality: round(q, 1),
				Sync: int(f.Sync), Data: f.Data, Have: f.Carrier.Have, Auto: f.Carrier.Autotune, Offset: round(f.Carrier.OffsetHz, 1),
				Audio: round(f.Carrier.AudioHz, 1), CPU: round(f.CPU, 1)}
			s.mu.Lock()
			s.last = m
			s.mu.Unlock()
			s.hub.Publish("meters", m, true, false)
			if sp := f.Spectrum; sp != nil {
				var raw []int16
				if s.run.Config().Meters.SpectrumRaw {
					raw = dB(sp.Raw)
				}
				s.hub.Publish("spectrum", spectrumMsg{sp.StartHz, sp.StepHz, sp.CarrierHz, dB(sp.Filtered), raw, round(sp.FullScaleDB, 1)}, true, false)
			}
		},
		Text: func(text string, t time.Time) {
			s.hub.AddText(t, text)
			s.hub.Publish("text", textChunk{t.UnixMilli(), text}, false, false)
		},
		Line: func(text string, q float64, t time.Time) {
			l := lineMsg{t.UnixMilli(), round(q, 0), text}
			s.hub.AddLine(l)
			s.hub.Publish("line", l, false, false)
		},
		Record: func(r store.Record) {
			m := brief(r, false)
			m.Action = "new"
			s.hub.ClearLines()
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
	mux.HandleFunc("GET /api/audio-devices", s.admin(tunnel, s.devices))
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
	writeJSON(w, 200, map[string]any{"fields": out})
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
	oldServer := s.run.Config().Server
	oldPublic := s.run.Config().Operator.Public
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
	for k := range body.Changes {
		names = append(names, k)
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

// publishControls tells every client the new control settings (the device name is left out; administrators read it
// from the settings).
func (s *Server) publishControls() { s.hub.Publish("controls", s.controls(false), false, false) }

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
	switch name {
	case "resync":
		if !s.run.Resync() {
			err = errors.New("the channel is not running")
		}
	case "polarity":
		var t string
		var p sitor.Polarity
		if t, err = asString(); err == nil {
			if err = p.UnmarshalText([]byte(t)); err == nil {
				err = s.run.SetPolarity(p)
			}
		}
	case "uos":
		var b bool
		if b, err = asBool(); err == nil {
			err = s.run.SetUnshiftOnSpace(b)
		}
	case "autotune":
		var b bool
		if b, err = asBool(); err == nil {
			err = s.run.SetAutotune(b)
		}
	case "carrier":
		var f float64
		if f, err = asFloat(); err == nil {
			if f < 200 || f > 2600 {
				err = errors.New("carrier must be between 200 and 2600 Hz")
			} else {
				err = s.run.SetCarrier(f)
			}
		}
	case "comment":
		var t string
		if t, err = asString(); err == nil {
			err = s.run.SetComment(strings.TrimSpace(t))
		}
	case "frequency":
		var f float64
		if f, err = asFloat(); err == nil {
			if f != 518 && f != 490 && f != 424 {
				err = errors.New("frequency must be 518, 490 or 424 (kHz)")
			} else {
				var restart bool
				restart, err = s.run.Modify(func(c *config.Config) error { c.Channel.FrequencyHz = f * 1000; return nil })
				if err == nil && restart {
					s.run.Log("info", fmt.Sprintf("channel changed to %.0f kHz", f))
					s.run.Restart()
				}
			}
		}
	case "device":
		var t string
		if t, err = asString(); err == nil {
			var restart bool
			restart, err = s.run.Modify(func(c *config.Config) error { c.Input.Source, c.Input.Device = "device", t; return nil })
			if err == nil && restart {
				s.run.Log("info", "audio input changed to "+t)
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
func (s *Server) smoothQuality(q float64, t time.Time) float64 {
	tau := s.run.Config().Meters.QualityTauS
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := t.Sub(s.qAt).Seconds()
	if tau <= 0 || s.qAt.IsZero() || dt <= 0 || dt > 10*tau+5 {
		s.qSmooth = q
	} else {
		s.qSmooth += (q - s.qSmooth) * (1 - math.Exp(-dt/tau))
	}
	s.qAt = t
	return s.qSmooth
}

// smoothLevel is the first-order low-pass of the input level gauge, applied to the level in dB
// (time constant meters.level_display_tau_s). Peak and RMS are linear amplitudes in and out.
func (s *Server) smoothLevel(peak, rms float64, t time.Time) (float64, float64) {
	tau := s.run.Config().Meters.LevelTauS
	toDB := func(v float64) float64 {
		if v < 1e-5 {
			return -100
		}
		return 20 * math.Log10(v)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := t.Sub(s.lAt).Seconds()
	p, r := toDB(peak), toDB(rms)
	if tau <= 0 || s.lAt.IsZero() || dt <= 0 || dt > 10*tau+5 {
		s.lPeak, s.lRMS = p, r
	} else {
		a := 1 - math.Exp(-dt/tau)
		s.lPeak += (p - s.lPeak) * a
		s.lRMS += (r - s.lRMS) * a
	}
	s.lAt = t
	if peak <= 0 {
		return 0, 0
	}
	return math.Pow(10, s.lPeak/20), math.Pow(10, s.lRMS/20)
}
