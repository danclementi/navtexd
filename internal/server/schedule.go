package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"navtex/internal/config"
	"navtex/internal/schedule"
)

// The scheduler (Options, Scheduler): entries set a setting to a value at a time of day, every day. The server runs the schedule,
// so it works with no browser open. The settings that can be scheduled are listed by scheduleTargets; more can be added there.

type schedTarget struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// scheduleTargets lists the settings the schedule can change, with the names the user knows them by.
func scheduleTargets(c config.Config) []schedTarget {
	name := func(n, dflt string) string {
		if n != "" {
			return n
		}
		return dflt
	}
	out := []schedTarget{{"operator.comment", "Operator comment"}}
	out = append(out, schedTarget{"channel.comment", name(c.Channel.Name, "Channel 1") + " comment"})
	for k := range c.Extra {
		out = append(out, schedTarget{fmt.Sprintf("extra.%d.channel.comment", k), name(c.Extra[k].Channel.Name, fmt.Sprintf("Channel %d", k+2)) + " comment"})
	}
	return out
}

func (s *Server) scheduleRoutes(mux *http.ServeMux, tunnel bool) {
	mux.HandleFunc("GET /api/schedule", s.admin(tunnel, s.scheduleGet))
	mux.HandleFunc("PUT /api/schedule", s.admin(tunnel, s.schedulePut))
}

type schedRow struct {
	schedule.Entry
	NextUTC   string `json:"next_utc,omitempty"`
	NextLocal string `json:"next_local,omitempty"`
}

func (s *Server) scheduleView() map[string]any {
	z, now := s.zone(), time.Now()
	rows := []schedRow{}
	for _, e := range s.sched.All() {
		r := schedRow{Entry: e}
		if t, ok := e.Next(z, now); ok && !e.Off {
			r.NextUTC, r.NextLocal = t.UTC().Format("2006-01-02 15:04"), t.In(z.Loc).Format("2006-01-02 15:04")
		}
		rows = append(rows, r)
	}
	return map[string]any{"entries": rows, "targets": scheduleTargets(s.run.Config()), "sun": z.HavePos, "max": schedule.MaxEntries, "max_value": schedule.MaxValue}
}

func (s *Server) scheduleGet(w http.ResponseWriter, r *http.Request, id Identity) {
	writeJSON(w, 200, s.scheduleView())
}

func (s *Server) schedulePut(w http.ResponseWriter, r *http.Request, id Identity) {
	var body struct {
		Entries []schedule.Entry `json:"entries"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.scheduleCheck(body.Entries); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.sched.Replace(body.Entries); err != nil {
		fail(w, 400, err)
		return
	}
	s.run.Log("info", fmt.Sprintf("schedule changed: %d entries", len(body.Entries)))
	writeJSON(w, 200, s.scheduleView())
}

// scheduleCheck refuses what the entries alone cannot show to be wrong: a setting that cannot be scheduled, or a sun time with no
// receiving position to work it out from.
func (s *Server) scheduleCheck(es []schedule.Entry) error {
	c := s.run.Config()
	known := map[string]bool{}
	for _, t := range scheduleTargets(c) {
		known[t.Path] = true
	}
	haveSun := c.Operator.Zone().HavePos
	for i, e := range es {
		if !known[e.Param] {
			return fmt.Errorf("line %d: %q cannot be scheduled", i+1, e.Param)
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
		if e.UsesSun() && !haveSun {
			return fmt.Errorf("line %d: sunrise and sunset need the receiving position (Options, Station)", i+1)
		}
	}
	return nil
}

// scheduleApply sets the setting of one occurrence, unless it already has the value.
func (s *Server) scheduleApply(f schedule.Fire, why string) {
	cur := s.run.Config()
	for _, fld := range config.Fields(cur) {
		if fld.Path == f.E.Param {
			if v, ok := fld.Value.(string); ok && v == f.E.Value {
				return
			}
			break
		}
	}
	val, _ := json.Marshal(f.E.Value)
	_, err := s.run.Modify(func(c *config.Config) error { return config.Set(c, f.E.Param, string(val)) })
	label := f.E.Param
	for _, t := range scheduleTargets(cur) {
		if t.Path == f.E.Param {
			label = t.Label
		}
	}
	if err != nil {
		s.run.Log("warn", fmt.Sprintf("schedule: %s could not be set to %q: %v", label, f.E.Value, err))
		return
	}
	s.run.Log("info", fmt.Sprintf("schedule%s: %s set to %q (%s %s)", why, label, f.E.Value, f.E.Zone, f.E.At))
	s.publishControls()
}

// scheduleLoop applies the schedule until ctx ends. At the start every setting takes the value of its latest occurrence, as the
// program may have been off at the time; after that each occurrence is applied when its minute comes (and one missed while the
// computer slept is applied late, in order). Changing the list does not apply anything by itself.
func (s *Server) scheduleLoop(ctx context.Context) {
	if s.sched == nil {
		return
	}
	now := time.Now()
	s.scheduleStart(now)
	last := now
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now = time.Now()
			if now.Before(last) { // the clock was set back
				last = now
				continue
			}
			for _, f := range schedule.Due(s.sched.All(), s.zone(), last, now) {
				s.scheduleApply(f, "")
			}
			last = now
		}
	}
}

// scheduleStart gives every scheduled setting the value of its latest occurrence.
func (s *Server) scheduleStart(now time.Time) {
	cur := schedule.Current(s.sched.All(), s.zone(), now)
	fires := make([]schedule.Fire, 0, len(cur))
	for _, f := range cur {
		fires = append(fires, f)
	}
	sortFires(fires)
	for _, f := range fires {
		s.scheduleApply(f, " (at start)")
	}
}

func sortFires(fs []schedule.Fire) {
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && fs[j].T.Before(fs[j-1].T); j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}
