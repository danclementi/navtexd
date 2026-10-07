package iq

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readAll(t *testing.T, s Source, block int) []complex64 {
	t.Helper()
	var out []complex64
	buf := make([]complex64, block)
	for {
		n, err := s.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// wavBytes builds a minimal PCM16 WAV with an optional JUNK chunk and a caller-chosen data size field.
func wavBytes(channels int, rate uint32, samples []int16, junk bool, dataField uint32) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	if junk {
		b.WriteString("JUNK")
		binary.Write(&b, binary.LittleEndian, uint32(5)) // odd size => pad byte
		b.Write([]byte{1, 2, 3, 4, 5, 0})
	}
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	for _, v := range []any{uint16(1), uint16(channels), rate, rate * uint32(2*channels), uint16(2 * channels), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, dataField)
	binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

func writeTmp(t *testing.T, name string, data []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQuirks(t *testing.T) {
	samples := []int16{100, -200, 300, -400, 500, -600} // 3 stereo frames
	for _, c := range []struct {
		name  string
		field uint32
	}{{"exact", 12}, {"zero", 0}, {"ffff", 0xFFFFFFFF}, {"toolarge", 1 << 20}} {
		t.Run(c.name, func(t *testing.T) {
			p := writeTmp(t, "x_504000HZ.wav", wavBytes(2, 62500, samples, true, c.field))
			w, err := OpenWav(p, WavOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if w.SampleRate() != 62500 || w.CentreHz() != 504000 || w.Kind() != IQ {
				t.Fatalf("rate %v centre %v kind %v", w.SampleRate(), w.CentreHz(), w.Kind())
			}
			got := readAll(t, w, 2)
			if len(got) != 3 {
				t.Fatalf("got %d frames, want 3", len(got))
			}
			if got[1] != complex(300.0/32768, -400.0/32768) {
				t.Fatalf("frame 1 = %v", got[1])
			}
		})
	}
}

func TestSwapAndMono(t *testing.T) {
	p := writeTmp(t, "a_1kHz.wav", wavBytes(2, 8000, []int16{1000, -2000}, false, 4))
	w, err := OpenWav(p, WavOptions{SwapIQ: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if got := readAll(t, w, 4); got[0] != complex(-2000.0/32768, 1000.0/32768) {
		t.Fatalf("swap: %v", got[0])
	}
	pm := writeTmp(t, "mono.wav", wavBytes(1, 8000, []int16{16384, -16384}, false, 4))
	m, err := OpenWav(pm, WavOptions{CentreHz: 516500})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Kind() != Real || m.CentreHz() != 516500 {
		t.Fatalf("mono kind %v centre %v", m.Kind(), m.CentreHz())
	}
	if got := readAll(t, m, 4); len(got) != 2 || got[0] != complex(0.5, 0) || got[1] != complex(-0.5, 0) {
		t.Fatalf("mono samples %v", got)
	}
}

func TestErrors(t *testing.T) {
	if _, err := OpenWav(writeTmp(t, "junk.wav", []byte("hello world, not a wav")), WavOptions{}); err == nil {
		t.Fatal("expected error for non-WAV")
	}
	// stereo with no centre in the name and none given
	if _, err := OpenWav(writeTmp(t, "nocentre.wav", wavBytes(2, 8000, []int16{1, 2}, false, 4)), WavOptions{}); err == nil {
		t.Fatal("expected error for missing centre")
	}
}

func TestParseCentre(t *testing.T) {
	for name, want := range map[string]float64{
		`C:\rec\SDRconnect_IQ_20260928_120000_501500HZ.wav`: 501500,
		"cap_504kHz.wav": 504000, "x_518.5 kHz_y.wav": 518500,
	} {
		if got, ok := ParseCentre(name); !ok || got != want {
			t.Errorf("%s: got %v %v want %v", name, got, ok, want)
		}
	}
	if _, ok := ParseCentre("plain.wav"); ok {
		t.Error("plain.wav should have no centre")
	}
}

// Golden-vector checks; skipped when testdata/ is absent (the generator is not part of this repository).
type meta struct {
	SampleRate float64 `json:"sample_rate"`
	CentreHz   float64 `json:"centre_hz"`
	NFrames    int64   `json:"n_frames"`
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"clean", "nominal", "usb_real"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("..", "..", "testdata")
			js, err := os.ReadFile(filepath.Join(dir, name+".json"))
			if err != nil {
				t.Skip("testdata not generated")
			}
			var m meta
			if err := json.Unmarshal(js, &m); err != nil {
				t.Fatal(err)
			}
			w, err := OpenWav(filepath.Join(dir, name+".wav"), WavOptions{CentreHz: m.CentreHz, HasCentre: true})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if w.SampleRate() != m.SampleRate || w.Frames() != m.NFrames {
				t.Fatalf("rate/frames %v/%v want %v/%v", w.SampleRate(), w.Frames(), m.SampleRate, m.NFrames)
			}
			a := readAll(t, w, 4096)
			if int64(len(a)) != m.NFrames {
				t.Fatalf("read %d frames, want %d", len(a), m.NFrames)
			}
			// Block-size invariance: 1 and a large odd size must give identical samples.
			w2, _ := OpenWav(filepath.Join(dir, name+".wav"), WavOptions{CentreHz: m.CentreHz, HasCentre: true})
			defer w2.Close()
			b := readAll(t, w2, 100003)
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("sample %d differs across block sizes", i)
				}
			}
			for i, v := range a[:1000] {
				if math.IsNaN(float64(real(v))) || math.Abs(float64(real(v))) > 1 {
					t.Fatalf("sample %d out of range: %v", i, v)
				}
			}
		})
	}
	// Known first sample of clean.wav (bytes b1 00 3e fa): I=177, Q=-1474.
	if w, err := OpenWav(filepath.Join("..", "..", "testdata", "clean.wav"), WavOptions{CentreHz: 504000, HasCentre: true}); err == nil {
		defer w.Close()
		buf := make([]complex64, 1)
		w.Read(buf)
		if buf[0] != complex(177.0/32768, -1474.0/32768) {
			t.Fatalf("first sample %v", buf[0])
		}
	}
}

func TestInferStartTime(t *testing.T) {
	// stamp in the file name is local time
	p := writeTmp(t, "SDRconnect_IQ_20260928_204010_501500HZ.wav", []byte("x"))
	got, how, err := InferStartTime(p, 100)
	if err != nil || !strings.HasPrefix(how, "file name") {
		t.Fatalf("%v %q", err, how)
	}
	if want := time.Date(2026, 9, 28, 20, 40, 10, 0, time.Local); !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// otherwise: modification time minus duration
	q := writeTmp(t, "capture.wav", []byte("x"))
	mt := time.Date(2026, 1, 26, 10, 40, 0, 0, time.Local)
	os.Chtimes(q, mt, mt)
	got, how, err = InferStartTime(q, 61)
	if err != nil || strings.HasPrefix(how, "file name") || !got.Equal(mt.Add(-61*time.Second)) || got.Location() != time.UTC {
		t.Fatalf("%v %v %q", got, err, how)
	}
}
