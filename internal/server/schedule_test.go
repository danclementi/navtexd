package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"navtex/internal/config"
	"navtex/internal/schedule"
)

func TestSchedule(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.TimeZone = "UTC"
	cfg.Channel.Name = "Wire"
	cfg.Operator.Comment = "start"
	s, _, _, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()

	code, v := do(t, admin, "GET", "/api/schedule", nil, nil)
	targets := v["targets"].([]any)
	if code != 200 || len(targets) != 5 || targets[0].(map[string]any)["path"] != "operator.comment" || targets[1].(map[string]any)["label"] != "Wire comment" || targets[4].(map[string]any)["path"] != "extra.2.channel.comment" {
		t.Fatalf("targets: %d %v", code, v)
	}
	if code, _ = do(t, tunnel, "GET", "/api/schedule", nil, nil); code == 200 {
		t.Error("a viewer read the schedule")
	}
	put := func(es ...map[string]any) (int, map[string]any) {
		return do(t, admin, "PUT", "/api/schedule", map[string]any{"entries": es}, nil)
	}
	e := func(param, zone, at, value string) map[string]any {
		return map[string]any{"param": param, "zone": zone, "at": at, "value": value}
	}
	for name, bad := range map[string]map[string]any{
		"unknown setting": e("receiver.blanker_threshold", "utc", "10:00", "1"),
		"sun, no place":   e("operator.comment", "local", "sunset", "x"),
		"sun in utc":      e("operator.comment", "utc", "sunset", "x"),
		"bad time":        e("operator.comment", "utc", "7pm", "x"),
	} {
		if code, _ := put(bad); code != 400 {
			t.Errorf("%s: code %d", name, code)
		}
	}
	code, v = put(e("operator.comment", "utc", "10:00", "dipole"), e("channel.comment", "utc", "22:00", "loop"), e("extra.1.channel.comment", "local", "midnight+1h", "x"))
	rows, _ := v["entries"].([]any)
	if code != 200 || len(rows) != 3 || rows[2].(map[string]any)["id"].(float64) != 3 || rows[0].(map[string]any)["next_utc"] == "" {
		t.Fatalf("put: %d %v", code, v)
	}
	if got := schedule.Open(s.run.DataDir()).All(); len(got) != 3 {
		t.Errorf("file: %v", got)
	}

	// at start every setting takes the value of its latest occurrence; the change is applied and logged, and an equal value is left alone
	s.scheduleStart(time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC))
	c := s.run.Config()
	if c.Operator.Comment != "dipole" || c.Channel.Comment != "loop" || c.Extra[1].Channel.Comment != "x" {
		t.Errorf("after start: %q %q %q", c.Operator.Comment, c.Channel.Comment, c.Extra[1].Channel.Comment)
	}
	s.scheduleApply(schedule.Fire{T: time.Now(), E: schedule.Entry{Param: "operator.comment", Zone: "utc", At: "22:00", Value: "night"}}, "")
	if s.run.Config().Operator.Comment != "night" {
		t.Error("an occurrence was not applied")
	}
}
