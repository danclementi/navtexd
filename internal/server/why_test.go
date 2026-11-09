package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"navtex/internal/config"
)

func chipTexts(why map[string]any) string {
	var t []string
	for _, c := range why["chips"].([]any) {
		t = append(t, c.(map[string]any)["t"].(string))
	}
	return strings.Join(t, "|")
}

func TestMessageWhy(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()
	when := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	ok := addMsg(t, st, s.cat, "F", when, "SOUTH OF NANTUCKET SHOALS", "GALE WARNING")
	no := addMsg(t, st, s.cat, "E", when.Add(time.Hour), "NOTHING USEFUL")
	get := func(srv *httptest.Server, id int64) map[string]any {
		_, j := do(t, srv, "GET", fmt.Sprintf("/api/messages/%d", id), nil, nil)
		w, _ := j["why"].(map[string]any)
		if w == nil {
			t.Fatalf("no why in %v", j)
		}
		return w
	}
	w := get(admin, ok.ID)
	if w["method"] == "UNID" || w["differs"] != nil || !strings.Contains(chipTexts(w), "B1 F") {
		t.Errorf("identified record: %v", w)
	}
	if c := w["cands"].([]any); len(c) == 0 || c[0].(map[string]any)["chosen"] != true || len(c[0].(map[string]any)["terms"].([]any)) == 0 {
		t.Errorf("candidates: %v", w["cands"])
	}
	hasDist := func(w map[string]any) bool {
		for _, c := range w["cands"].([]any) {
			for _, tm := range c.(map[string]any)["terms"].([]any) {
				if tm.(map[string]any)["k"] == "dist" {
					return true
				}
			}
		}
		return false
	}
	if !hasDist(w) {
		t.Error("an administrator gets the distance term")
	}
	if hasDist(get(tunnel, ok.ID)) {
		t.Error("a viewer without location rights got a distance term")
	}
	w = get(admin, no.ID)
	if w["method"] != "UNID" || w["reason"] == nil || !strings.Contains(chipTexts(w), "UNID") {
		t.Errorf("unidentified record: %v", w)
	}
}
