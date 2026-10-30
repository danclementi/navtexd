package clip

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"navtex/internal/iq"
)

func TestRingWrapAndFile(t *testing.T) {
	r, got := New(1000, iq.Real, 4, 1) // 5 s ring
	if got != 5 {
		t.Fatalf("capacity %v", got)
	}
	t0 := time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)
	var n int
	feed := func(sec int) bool {
		s := make([]complex64, 1000*sec)
		for i := range s {
			s[i] = complex(float32(n+i), 0)
		}
		n += len(s)
		return r.Write(s, t0.Add(time.Duration(n)*time.Millisecond))
	}
	if _, err := r.Request(); err == nil {
		t.Error("request before any input accepted")
	}
	feed(7) // the ring has wrapped
	if _, err := r.Request(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Request(); err == nil {
		t.Error("second request accepted")
	}
	if feed(0) || !feed(1) {
		t.Fatal("completion not reported after the post-roll")
	}
	fr, m := r.Take()
	if len(fr) != 5000 || fr[0] != 3000 || fr[4999] != 7999 {
		t.Fatalf("clip content %d %v %v", len(fr), fr[0], fr[4999])
	}
	if m.StartUTC != "2026-10-03 05:00:03.000" {
		t.Errorf("start %s", m.StartUTC)
	}
	m.CentreHz = 517000
	dir := t.TempDir()
	name, err := Write(dir, fr, m)
	if err != nil {
		t.Fatal(err)
	}
	// the file reads back through the normal WAV source, and the sidecar is found
	w, err := iq.OpenWav(filepath.Join(dir, name), iq.WavOptions{CentreHz: 517000, HasCentre: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close() // Windows cannot delete the temporary folder while the file is open
	buf := make([]complex64, 10)
	if k, _ := w.Read(buf); k != 10 || math.Abs(float64(real(buf[1]))-3001) > 0 {
		t.Errorf("read back %d %v", k, buf[1])
	}
	if mm, ok := ReadMeta(filepath.Join(dir, name)); !ok || mm.Frames != 5000 || mm.CentreHz != 517000 {
		t.Errorf("meta %v %v", mm, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, name[:len(name)-4]+".json")); err != nil {
		t.Error(err)
	}
}

func TestRequestWithGroupAndSuffix(t *testing.T) {
	r, _ := New(1000, iq.Real, 4, 1)
	t0 := time.Date(2026, 10, 3, 5, 0, 0, 0, time.UTC)
	r.Write(make([]complex64, 6000), t0)
	if _, err := r.RequestWith(Meta{Note: "n", Group: "g1", Channel: 2, Served: []int{2, 4}}); err != nil {
		t.Fatal(err)
	}
	r.Write(make([]complex64, 1000), t0.Add(time.Second))
	_, m := r.Take()
	if m.Group != "g1" || m.Channel != 2 || len(m.Served) != 2 || m.Note != "n" {
		t.Fatalf("meta %+v", m)
	}
	m.Suffix = "_ch2"
	if n := Name(m); n[len(n)-len("_ch2.wav"):] != "_ch2.wav" && n[len(n)-len("_ch2"):] != "_ch2" {
		t.Errorf("name %s", n)
	}
}
