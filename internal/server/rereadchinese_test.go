package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"navtex/internal/config"
	"navtex/internal/store"
)

// Messages that build 173 stored with their header digits as letters (EAIY for EA86) are read again: the header number and the
// text get their digits and a plain NNNN in the line of the end marker is no characters; the received letters stay, other
// channels are not touched, and Undo takes it back.
func TestRereadChinese(t *testing.T) {
	cfg := config.Default()
	cfg.Input.File.Path = "/nonexistent.wav"
	cfg.Input.Source = "file"
	s, _, st, _ := setup(t, cfg)
	srv := httptest.NewServer(s.handler(false))
	defer srv.Close()
	at := time.Date(2026, 11, 18, 12, 40, 0, 0, time.UTC)
	add := func(hz int, serial string, lines ...string) store.Record {
		r := store.Record{Channel: hz, Kind: "message", B1: "E", B2: "A", Serial: serial, Received: at, End: at.Add(time.Minute), Quality: 90}
		for _, l := range lines {
			r.Lines = append(r.Lines, store.Line{Time: at, Text: l})
		}
		s.run.Correct(&r)
		saved, err := st.Add(r)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	old := add(486000, "IY", "ZCZC EAIY", "WYQQQIQYQI", "AHWAHWAHWAIZAIZAIZAIZNNNN")
	good := add(486000, "23", "ZCZC EA23", "AHWAHWAHWAIZAIZAIZAIZ", "NNNN")
	other := add(518000, "IY", "ZCZC EAIY", "ORDINARY TEXT")
	// (the record was corrected by the program as it is now; put back what the earlier build had made of it)
	old.Corrected = "ZCZC EAIY\n□□□□\n１１１ＮＮＮＮ腚腚"
	if _, err := st.Update(old); err != nil {
		t.Fatal(err)
	}
	code, j := do(t, srv, "POST", "/api/maintenance/reread-chinese", map[string]any{"dry": true}, nil)
	res := j["result"].(map[string]any)
	if code != 200 || res["checked"].(float64) != 2 || res["changed"].(float64) != 1 || res["serials"].(float64) != 1 {
		t.Fatalf("dry: %d %v", code, j)
	}
	if got, _ := st.Get(old.ID); got.Serial != "IY" {
		t.Fatalf("a dry run changed the record: %+v", got)
	}
	code, j = do(t, srv, "POST", "/api/maintenance/reread-chinese", map[string]any{}, nil)
	if code != 200 || j["result"].(map[string]any)["changed"].(float64) != 1 || j["backup"] == nil {
		t.Fatalf("run: %d %v", code, j)
	}
	got, _ := st.Get(old.ID)
	if got.Serial != "86" || !strings.Contains(got.Corrected, "ZCZC EA86\n2611181618\n１１１ＮＮＮＮ NNNN") || got.Lines[0].Text != "ZCZC EAIY" {
		t.Errorf("after: serial %q, corrected %q, raw %q", got.Serial, got.Corrected, got.Lines[0].Text)
	}
	if g, _ := st.Get(other.ID); g.Serial != "IY" {
		t.Errorf("a 518 kHz message was changed: %+v", g)
	}
	if g, _ := st.Get(good.ID); g.Serial != "23" {
		t.Errorf("a good message was changed: %+v", g)
	}
	code, j = do(t, srv, "POST", "/api/maintenance/reread-chinese", map[string]any{"dry": true}, nil)
	if j["result"].(map[string]any)["changed"].(float64) != 0 {
		t.Errorf("a second run still finds changes: %v", j)
	}
	if code, _ = do(t, srv, "POST", "/api/messages/undo", nil, nil); code != 200 {
		t.Fatalf("undo: %d", code)
	}
	if g, _ := st.Get(old.ID); g.Serial != "IY" {
		t.Errorf("Undo did not bring back the serial: %+v", g)
	}
}
