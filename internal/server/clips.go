package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"navtex/internal/clip"
)

var clipName = regexp.MustCompile(`^clip_[0-9]{8}T[0-9]{6}Z_[0-9]+Hz\.(wav|json)$`)

func (s *Server) clipRoutes(mux *http.ServeMux, tunnel bool) {
	mux.HandleFunc("POST /api/clips", s.admin(tunnel, s.clipSave))
	mux.HandleFunc("GET /api/clips", s.admin(tunnel, s.clipList))
	mux.HandleFunc("GET /api/clips/{name}", s.admin(tunnel, s.clipGet))
	mux.HandleFunc("DELETE /api/clips/{name}", s.admin(tunnel, s.clipDelete))
}

func (s *Server) clipSave(w http.ResponseWriter, r *http.Request, id Identity) {
	ch, err := rcvOf(r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	wait, err := s.run.SaveClip(ch)
	if err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "wait_s": wait})
}

type clipInfo struct {
	Name    string  `json:"name"`
	Bytes   int64   `json:"bytes"`
	StartMs int64   `json:"start"`
	Seconds float64 `json:"s"`
	Kind    string  `json:"kind"`
	Channel float64 `json:"ch"`
}

func (s *Server) clipList(w http.ResponseWriter, r *http.Request, id Identity) {
	dir := s.run.ClipDir()
	ents, _ := os.ReadDir(dir)
	out := []clipInfo{}
	for _, e := range ents {
		n := e.Name()
		if !clipName.MatchString(n) || !strings.HasSuffix(n, ".wav") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		ci := clipInfo{Name: n, Bytes: fi.Size()}
		if m, ok := clip.ReadMeta(filepath.Join(dir, n)); ok {
			if t, err := time.Parse(clip.TimeFormat, m.StartUTC); err == nil {
				ci.StartMs = t.UnixMilli()
			}
			ci.Seconds, ci.Kind, ci.Channel = m.Seconds, m.Kind, m.ChannelHz
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	writeJSON(w, 200, map[string]any{"clips": out, "dir": dir})
}

func (s *Server) clipFile(w http.ResponseWriter, r *http.Request) (string, bool) {
	n := r.PathValue("name")
	if !clipName.MatchString(n) {
		fail(w, 400, fmt.Errorf("bad clip name"))
		return "", false
	}
	return filepath.Join(s.run.ClipDir(), n), true
}

func (s *Server) clipGet(w http.ResponseWriter, r *http.Request, id Identity) {
	p, ok := s.clipFile(w, r)
	if !ok {
		return
	}
	if _, err := os.Stat(p); err != nil {
		fail(w, 404, fmt.Errorf("no such clip"))
		return
	}
	if strings.HasSuffix(p, ".json") {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "audio/wav")
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	http.ServeFile(w, r, p)
}

func (s *Server) clipDelete(w http.ResponseWriter, r *http.Request, id Identity) {
	p, ok := s.clipFile(w, r)
	if !ok {
		return
	}
	base := strings.TrimSuffix(p, filepath.Ext(p))
	e1, e2 := os.Remove(base+".wav"), os.Remove(base+".json")
	if e1 != nil && e2 != nil {
		fail(w, 404, fmt.Errorf("no such clip"))
		return
	}
	s.run.Log("info", "audio clip deleted: "+filepath.Base(base))
	writeJSON(w, 200, map[string]any{"ok": true})
}
