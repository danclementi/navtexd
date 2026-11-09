package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"navtex/internal/config"
)

func TestPruneFilterRules(t *testing.T) {
	for _, c := range []struct {
		mode, before string
		km, freq     float64
		ok           bool
	}{
		{"date", "2026-06-21", 0, 0, true},
		{"date", "", 0, 0, false},
		{"date", "2026-6-21", 0, 0, false},
		{"distance", "", 2000, 490, true},
		{"distance", "", 0, 0, false},
		{"distance", "", -5, 0, false},
		{"both", "2026-06-21", 2500, 518, true},
		{"both", "2026-06-21", 0, 0, false},
		{"unid", "", 0, 0, true},
		{"", "", 0, 0, false},
		{"date", "2026-06-21", 0, -1, false},
	} {
		if _, err := pruneFilter(c.mode, c.before, c.km, c.freq); (err == nil) != c.ok {
			t.Errorf("%+v: err %v", c, err)
		}
	}
	f, _ := pruneFilter("date", "2026-06-21", 0, 4209.5)
	if !f.OmitUnid || f.Channel != 4209500 || !f.To.Equal(time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date filter %+v", f)
	}
}

func TestPrune(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	tunnel := httptest.NewServer(s.handler(true))
	defer tunnel.Close()

	old := time.Date(2026, 5, 1, 0, 50, 0, 0, time.UTC)
	near := addMsg(t, st, s.cat, "F", old, "SOUTH OF NANTUCKET SHOALS", "GALE WARNING") // identified, about 900 km away
	unid := addMsg(t, st, s.cat, "E", old.Add(time.Hour), "NOTHING USEFUL")             // no station
	newer := addMsg(t, st, s.cat, "F", old.AddDate(0, 3, 0), "SOUTH OF NANTUCKET SHOALS", "GALE WARNING")
	if near.DistanceKm == 0 || unid.Station != "UNID" || newer.DistanceKm == 0 {
		t.Fatalf("set-up: %+v %+v %+v", near, unid, newer)
	}
	post := func(srv *httptest.Server, body map[string]any) (int, map[string]any) {
		code, j := do(t, srv, "POST", "/api/maintenance/prune", body, nil)
		return code, j
	}

	if code, _ := post(tunnel, map[string]any{"mode": "unid", "dry": true}); code == 200 {
		// the tunnel is the viewer side: it must not be allowed without admin rights
		if st.Count() != 3 {
			t.Fatal("tunnel changed the log")
		}
	}
	if code, _ := post(admin, map[string]any{"mode": "bogus", "dry": true}); code != 400 {
		t.Errorf("bogus mode: %d", code)
	}

	// date: older than 1 July keeps the record without a station
	if _, j := post(admin, map[string]any{"mode": "date", "before": "2026-07-01", "dry": true}); j["matches"].(float64) != 1 {
		t.Errorf("date dry: %v", j)
	}
	if st.Count() != 3 {
		t.Fatal("a dry run deleted")
	}
	// distance: closer than 5000 km, a record without a station is never matched; channel limits
	if _, j := post(admin, map[string]any{"mode": "distance", "km": 5000, "dry": true}); j["matches"].(float64) != 2 {
		t.Errorf("distance dry: %v", j)
	}
	if _, j := post(admin, map[string]any{"mode": "distance", "km": 5000, "freq": 490, "dry": true}); j["matches"].(float64) != 0 {
		t.Errorf("other channel: %v", j)
	}
	if _, j := post(admin, map[string]any{"mode": "both", "before": "2026-07-01", "km": 5000, "dry": true}); j["matches"].(float64) != 1 {
		t.Errorf("both dry: %v", j)
	}
	if _, j := post(admin, map[string]any{"mode": "unid", "dry": true}); j["matches"].(float64) != 1 {
		t.Errorf("unid dry: %v", j)
	}

	// the real thing, with Undo
	_, j := post(admin, map[string]any{"mode": "both", "before": "2026-07-01", "km": 5000})
	if j["deleted"].(float64) != 1 || st.Count() != 2 || j["backup"] == "" {
		t.Fatalf("prune: %v count %d", j, st.Count())
	}
	if code, _ := do(t, admin, "POST", "/api/messages/undo", nil, nil); code != 200 || st.Count() != 3 {
		t.Errorf("undo: %d, count %d", code, st.Count())
	}
	// for good: Undo has nothing to bring back; compacting afterwards works
	_, j = post(admin, map[string]any{"mode": "unid", "erase": true, "compact": true})
	if j["deleted"].(float64) != 1 || st.Count() != 2 || j["size_after"] == nil {
		t.Fatalf("erase: %v", j)
	}
	if _, u := do(t, admin, "GET", "/api/messages/undo", nil, nil); u["available"] == true {
		if code, _ := do(t, admin, "POST", "/api/messages/undo", nil, nil); code == 200 && st.Count() != 2 {
			t.Errorf("an erased record came back: count %d", st.Count())
		}
	}
	if _, tj := do(t, admin, "GET", "/api/trash", nil, nil); tj["total"] != nil && tj["total"].(float64) != 0 {
		t.Errorf("trash after erase: %v", tj)
	}
}
