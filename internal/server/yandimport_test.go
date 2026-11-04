package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"navtex/internal/config"
	"navtex/internal/store"
)

// A YaND reception report posted to the import is recognized and read; the station of the report is placed in the station table.
func TestImportYaNDReport(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	cfg.Operator.LatDeg, cfg.Operator.LonDeg = 36.85, -76.3
	s, _, st, _ := setup(t, cfg)
	admin := httptest.NewServer(s.handler(false))
	defer admin.Close()
	body := "---\n20261002 0852 518 $04F NMF Boston,USA                   414 [LZ1AQ 1.2m dia.]\n" +
		"2026-10-02 08:52:42 102> ZCZC FA81\n2026-10-02 08:52:47 100> SECNY BNM 0510-26 \n2026-10-02 08:53:27 96> NNNN\n"
	post := func() map[string]any {
		req, _ := http.NewRequest("POST", admin.URL+"/api/database/import?mode=merge", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Navtex", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %v", resp.StatusCode, out)
		}
		return out
	}
	out := post()
	if out["source"] != "YaND report" || out["added"] != float64(1) {
		t.Fatalf("reply: %v", out)
	}
	recs, _ := st.Query(storeAll())
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	r := recs[0]
	if r.Station != "NMF Boston,USA" || r.StationID == 0 || r.Channel != 518000 || r.B1 != "F" || r.B2 != "A" || r.Serial != "81" || r.DistanceKm == 0 {
		t.Fatalf("record: %+v", r)
	}
	// the same report again adds nothing
	if out := post(); out["added"] != float64(0) || out["skipped"] != float64(1) {
		t.Fatalf("second import: %v", out)
	}
}

func storeAll() store.Filter { return store.Filter{} }
