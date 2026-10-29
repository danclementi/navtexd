package server

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticGzipAndETag(t *testing.T) {
	body := strings.Repeat("function f() { return 1; }\n", 100)
	h := staticHandler(fstest.MapFS{"app.js": {Data: []byte(body)}, "index.html": {Data: []byte("<html></html>")}}, true)
	req := httptest.NewRequest("GET", "/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "gzip" || w.Body.Len() >= len(body)/4 {
		t.Fatalf("not compressed: %v %d", w.Header(), w.Body.Len())
	}
	zr, _ := gzip.NewReader(w.Body)
	if got, _ := io.ReadAll(zr); string(got) != body {
		t.Fatal("content differs")
	}
	etag := w.Header().Get("ETag")
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("status %d", w.Code)
	}
	req = httptest.NewRequest("GET", "/app.js", nil) // no gzip accepted
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != body {
		t.Fatal("plain copy wrong")
	}
	if w = httptest.NewRecorder(); true {
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Body.String() != "<html></html>" {
			t.Fatal("index not served for /")
		}
		h.ServeHTTP(w, httptest.NewRequest("GET", "/../secret", nil))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/nothing.js", nil))
	if w.Code != 404 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestGzipAPI(t *testing.T) {
	h := gzipAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"a": strings.Repeat("x", 1000)})
	}))
	req := httptest.NewRequest("GET", "/api/records", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	zr, err := gzip.NewReader(w.Body)
	if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal(err, w.Header())
	}
	var m map[string]string
	if json.NewDecoder(zr).Decode(&m) != nil || len(m["a"]) != 1000 {
		t.Fatal("bad body")
	}
	for _, p := range []string{"/api/events", "/api/clips/x.wav", "/index.html"} {
		w = httptest.NewRecorder()
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(w, r)
		if w.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s was compressed", p)
		}
	}
}

// decode applies a compact frame to the previous one like the browser does.
func decode(t *testing.T, e event, prev []int8) (m compactMsg, q []int8) {
	t.Helper()
	if e.name != "spectrum2" || json.Unmarshal(e.data, &m) != nil {
		t.Fatal("bad event", e.name)
	}
	b, _ := base64.StdEncoding.DecodeString(m.D)
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
	if err != nil || len(raw) != m.N+m.M {
		t.Fatal(err, len(raw))
	}
	q = make([]int8, len(raw))
	for i, v := range raw {
		if m.Key == 1 {
			q[i] = int8(v)
		} else {
			q[i] = prev[i] + int8(v)
		}
	}
	return
}

func TestCompactFrames(t *testing.T) {
	var st specState
	mk := func(shift int16) spectrumMsg {
		f, c := make([]int16, 50), make([]int16, 50)
		for i := range f {
			f[i] = int16(-300+i*7) + shift
			c[i] = int16(-900 + i*3)
		}
		c[10] = -3000
		f[3] = -2000 // far below the range: clipped
		return spectrumMsg{Start: 100, Step: 5.86, Carrier: 1000, Filt: f, Chan: c, FS: 31.4, Rcv: 2}
	}
	var prev []int8
	for k := 0; k < 3*keyEvery; k++ {
		sm := mk(int16(k * 9))
		e, ok := st.encode(sm)
		if !ok {
			t.Fatal("encode failed")
		}
		m, q := decode(t, e, prev)
		prev = q
		if (k%keyEvery == 0) != (m.Key == 1) {
			t.Fatalf("frame %d: key=%d", k, m.Key)
		}
		for i, v := range sm.Filt {
			want := quant(v)
			if q[i] != want || (v > -2000 && absI(int(want)-int(v)/10) > 1 && want != -qMax) {
				t.Fatalf("frame %d filt[%d] = %d, sent %d", k, i, q[i], v)
			}
		}
		if q[len(sm.Filt)+10] != -128 {
			t.Fatal("no-data marker lost")
		}
		if m.Rcv != 2 || m.Start != 100 || m.FS != 31.4 {
			t.Fatalf("%+v", m)
		}
	}
	st = specState{} // a client that missed a frame gets a full one
	e, _ := st.encode(mk(0))
	if m, _ := decode(t, e, nil); m.Key != 1 {
		t.Error("not a key frame")
	}
}

func absI(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
