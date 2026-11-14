package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"navtex/internal/presets"
)

func (s *Server) presetRoutes(mux *http.ServeMux, tunnel bool) {
	mux.HandleFunc("GET /api/presets", s.viewer(tunnel, s.presetList))
	mux.HandleFunc("PUT /api/presets/{n}", s.admin(tunnel, s.presetSet))
	mux.HandleFunc("DELETE /api/presets/{n}", s.admin(tunnel, s.presetClear))
}

func (s *Server) presetList(w http.ResponseWriter, r *http.Request, id Identity) {
	writeJSON(w, 200, map[string]any{"slots": s.pre.All()})
}

func presetNumber(r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	return n, err == nil && n >= 1 && n <= presets.Slots
}

func (s *Server) presetSet(w http.ResponseWriter, r *http.Request, id Identity) {
	n, ok := presetNumber(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var f presets.Filter
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*presets.MaxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.pre.Set(n, &f); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"slots": s.pre.All()})
}

func (s *Server) presetClear(w http.ResponseWriter, r *http.Request, id Identity) {
	n, ok := presetNumber(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.pre.Set(n, nil); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"slots": s.pre.All()})
}
