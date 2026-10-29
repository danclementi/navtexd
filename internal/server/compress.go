package server

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// ---- compressed responses ----
// The page files are served with an ETag (so that a reload costs a 304) and gzip-compressed (built once for the
// embedded files). JSON answers of the API are compressed on the fly. Event streams and audio clips are never touched.

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return !strings.Contains(strings.ReplaceAll(q, " ", ""), "q=0")
		}
	}
	return false
}

type staticFile struct {
	raw, gz []byte
	etag    string
	ctype   string
}

// staticHandler serves the files of fsys. cache says that the files never change (the embedded ones): they are read and
// compressed once. Otherwise (development, files on disk) every request reads the file.
func staticHandler(fsys fs.FS, cache bool) http.Handler {
	var mu sync.Mutex
	files := map[string]*staticFile{}
	load := func(name string) *staticFile {
		if cache {
			mu.Lock()
			defer mu.Unlock()
			if f, ok := files[name]; ok {
				return f
			}
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			if cache {
				files[name] = nil
			}
			return nil
		}
		sum := sha256.Sum256(b)
		f := &staticFile{raw: b, etag: hex.EncodeToString(sum[:8]), ctype: mime.TypeByExtension(path.Ext(name))}
		if f.ctype == "" {
			f.ctype = "application/octet-stream"
		}
		if len(b) > 512 && !strings.HasPrefix(f.ctype, "image/") {
			var sb strings.Builder
			zw, _ := gzip.NewWriterLevel(&sb, gzip.BestCompression)
			zw.Write(b)
			zw.Close()
			if sb.Len() < len(b) {
				f.gz = []byte(sb.String())
			}
		}
		if cache {
			files[name] = f
		}
		return f
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || strings.HasSuffix(r.URL.Path, "/") {
			name = path.Join(name, "index.html")
		}
		f := load(name)
		if f == nil {
			http.NotFound(w, r)
			return
		}
		body, etag := f.raw, `"`+f.etag+`"`
		h := w.Header()
		h.Set("Vary", "Accept-Encoding")
		if f.gz != nil && acceptsGzip(r) {
			body, etag = f.gz, `"`+f.etag+`-gz"`
			h.Set("Content-Encoding", "gzip")
		}
		h.Set("ETag", etag)
		h.Set("Cache-Control", "no-cache")
		if m := r.Header.Get("If-None-Match"); m != "" && strings.Contains(m, etag) {
			delete(h, "Content-Encoding")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", f.ctype)
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			w.Write(body)
		}
	})
}

var gzPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

type gzipWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
}

func (g *gzipWriter) decide(code int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.ResponseWriter.Header()
	ct := h.Get("Content-Type")
	if code != http.StatusNoContent && code != http.StatusNotModified && h.Get("Content-Encoding") == "" &&
		(strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/")) && !strings.HasPrefix(ct, "text/event-stream") {
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Del("Content-Length")
		g.zw = gzPool.Get().(*gzip.Writer)
		g.zw.Reset(g.ResponseWriter)
	}
}

func (g *gzipWriter) WriteHeader(code int) {
	g.decide(code)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	g.decide(http.StatusOK)
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.zw != nil {
		g.zw.Flush()
	}
	http.NewResponseController(g.ResponseWriter).Flush()
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.zw != nil {
		g.zw.Close()
		gzPool.Put(g.zw)
	}
}

// gzipAPI compresses the JSON answers of the API for clients that accept gzip.
func gzipAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/") || p == "/api/events" || strings.HasPrefix(p, "/api/clips/") || r.Header.Get("Range") != "" || !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		g := &gzipWriter{ResponseWriter: w}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}
