package server

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"navtex/internal/astro"
	"navtex/internal/engine"
	"navtex/internal/geo"
	"navtex/internal/stations"
	"navtex/internal/store"
)

func (s *Server) extraRoutes(mux *http.ServeMux, tunnel bool) {
	mux.HandleFunc("GET /api/logging", s.viewer(tunnel, s.getLogging))
	mux.HandleFunc("PUT /api/logging", s.admin(tunnel, s.putLogging))
	mux.HandleFunc("GET /api/scope", s.viewer(tunnel, s.scope))
	mux.HandleFunc("GET /api/ephemeris", s.viewer(tunnel, s.ephemeris))
	mux.HandleFunc("GET /api/syslog.txt", s.admin(tunnel, s.syslogText))
}

// ---- selective logging ----

func (s *Server) getLogging(w http.ResponseWriter, r *http.Request, id Identity) {
	writeJSON(w, 200, map[string]any{"disabled": s.cat.Disabled()})
}

func (s *Server) putLogging(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Disabled []int `json:"disabled"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.cat.SetDisabled(body.Disabled); err != nil {
		fail(w, 500, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("selective logging: %d stations are not logged", len(s.cat.Disabled())))
	writeJSON(w, 200, map[string]any{"ok": true, "disabled": s.cat.Disabled()})
}

// ---- ephemeris ----

// dayIntervals returns the minutes of the UTC day (0..1440) during which the sun is above the horizon at a position.
func dayIntervals(day time.Time, lat, lon float64) [][2]int {
	const step = 2
	var out [][2]int
	start := -1
	for m := 0; m <= 1440; m += step {
		up := m < 1440 && astro.Elevation(day.Add(time.Duration(m)*time.Minute), lat, lon) > -0.833
		switch {
		case up && start < 0:
			start = m
		case !up && start >= 0:
			out = append(out, [2]int{start, m})
			start = -1
		}
	}
	return out
}

// ephemeris compares the daylight at the receiver and at a station with the station's transmission slots. It needs the
// receiver's position, which only administrators (or viewers, if allowed) may learn indirectly through it.
func (s *Server) ephemeris(w http.ResponseWriter, r *http.Request, id Identity) {
	if !s.withDist(id) {
		fail(w, http.StatusForbidden, errors.New("the ephemeris needs the receiver's position and is only shown on the local network"))
		return
	}
	op := s.run.Config().Operator
	if op.LatDeg == 0 && op.LonDeg == 0 {
		fail(w, 400, errors.New("set your latitude and longitude in the Options first"))
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("station"))
	st, ok := s.cat.Station(n)
	if !ok {
		fail(w, 404, errors.New("no such station"))
		return
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	if d := r.URL.Query().Get("date"); d != "" {
		t, err := parseTime(d)
		if err != nil {
			fail(w, 400, err)
			return
		}
		day = t.Truncate(24 * time.Hour)
	}
	var slots []int
	for _, c := range st.Times {
		if m, ok := stations.ParseClock(c); ok {
			slots = append(slots, m)
		}
	}
	writeJSON(w, 200, map[string]any{
		"date":    day.Format("2006-01-02"),
		"station": stationMsgFor(n, st, &op.LatDeg, &op.LonDeg),
		"local":   dayIntervals(day, op.LatDeg, op.LonDeg),
		"dx":      dayIntervals(day, st.Lat, st.Lon),
		"slots":   slots,
	})
}

func stationMsgFor(id int, x stations.Station, opLat, opLon *float64) stationMsg {
	m := stationMsg{ID: id, NavArea: x.NavArea, Freq: x.FreqKHz, B1: x.ID, Callsign: x.Callsign, Name: x.Name, CC: x.CountryCode, Country: x.Country,
		Lat: x.Lat, Lon: x.Lon, Times: x.Times, GID: nonNil(x.GID), UID: nonNil(x.UID)}
	if opLat != nil && (*opLat != 0 || *opLon != 0) {
		m.Km = float64(int(geo.DistanceKm(*opLat, *opLon, x.Lat, x.Lon) + 0.5))
		m.Brg = float64(int(geo.BearingDeg(*opLat, *opLon, x.Lat, x.Lon) + 0.5))
	}
	return m
}

// ---- system log as a file ----

func (s *Server) syslogText(w http.ResponseWriter, r *http.Request, id Identity) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="navtex-system-log.txt"`)
	for _, e := range s.st.Syslog(0) {
		lvl := ""
		if e.Level != "info" {
			lvl = strings.ToUpper(e.Level) + " "
		}
		fmt.Fprintf(w, "%s  %s%s\n", e.Time.Format("2006-01-02 15:04:05.000"), lvl, e.Text)
	}
}

var _ = store.ErrNotFound

// scope serves one oscilloscope sweep (any role; the data carries no position information).
// Query: span (s), trig (none|rise|fall), pre (fraction before the trigger), after (end of the previous sweep, s),
// points (maximum points per trace).
func (s *Server) scope(w http.ResponseWriter, r *http.Request, id Identity) {
	sc := s.run.Scope()
	if sc == nil {
		writeJSON(w, 200, map[string]any{"head": 0})
		return
	}
	q := r.URL.Query()
	f := func(k string, d float64) float64 {
		if v, err := strconv.ParseFloat(q.Get(k), 64); err == nil {
			return v
		}
		return d
	}
	span := math.Max(0.05, math.Min(2, f("span", 0.5)))
	pre := math.Max(0, math.Min(0.9, f("pre", 0.1)))
	trig := map[string]int{"rise": engine.TrigRise, "fall": engine.TrigFall}[q.Get("trig")]
	sw := sc.Sweep(span, trig, pre, f("after", 0), int(math.Max(100, math.Min(2000, f("points", 1000)))))
	if sw == nil {
		writeJSON(w, 200, map[string]any{"head": round(sc.Head(), 3)})
		return
	}
	writeJSON(w, 200, map[string]any{"head": round(sw.Head, 3), "sweep": sw})
}
