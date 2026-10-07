package iq

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"testing/iotest"
	"time"
)

func s16(vals ...int16) []byte {
	var b bytes.Buffer
	for _, v := range vals {
		binary.Write(&b, binary.LittleEndian, v)
	}
	return b.Bytes()
}

func drainSource(t *testing.T, s Source) []complex64 {
	t.Helper()
	var out []complex64
	buf := make([]complex64, 5)
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

func TestPCMRealOneByteAtATime(t *testing.T) {
	// a reader that returns single bytes splits samples across reads
	r := iotest.OneByteReader(bytes.NewReader(s16(16384, -16384, 0, 32767)))
	s, err := NewPCM(r, PCMOptions{Rate: 48000, Channels: 1, CentreHz: 517000})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := drainSource(t, s)
	want := []float32{0.5, -0.5, 0, 32767.0 / 32768}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i, w := range want {
		if real(got[i]) != w || imag(got[i]) != 0 {
			t.Errorf("sample %d = %v, want %v", i, got[i], w)
		}
	}
	if s.Kind() != Real || s.SampleRate() != 48000 || s.CentreHz() != 517000 {
		t.Error("source description wrong")
	}
}

func TestPCMIQAndSwap(t *testing.T) {
	data := s16(16384, -16384, 8192, 0)
	for _, swap := range []bool{false, true} {
		s, _ := NewPCM(bytes.NewReader(data), PCMOptions{Rate: 48000, Channels: 2, SwapIQ: swap})
		got := drainSource(t, s)
		s.Close()
		want := []complex64{complex(0.5, -0.5), complex(0.25, 0)}
		if swap {
			want = []complex64{complex(-0.5, 0.5), complex(0, 0.25)}
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] || s.Kind() != IQ {
			t.Errorf("swap=%v: got %v want %v", swap, got, want)
		}
	}
}

func TestPCMFloat(t *testing.T) {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []float32{0.25, -1})
	s, _ := NewPCM(&b, PCMOptions{Rate: 48000, Channels: 1, Format: F32LE})
	defer s.Close()
	got := drainSource(t, s)
	if len(got) != 2 || real(got[0]) != 0.25 || real(got[1]) != -1 {
		t.Errorf("got %v", got)
	}
}

func TestPCMCloseUnblocksRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	s, _ := NewPCM(pr, PCMOptions{Rate: 48000, Channels: 1})
	done := make(chan error, 1)
	go func() { _, err := s.Read(make([]complex64, 8)); done <- err }()
	time.Sleep(50 * time.Millisecond)
	s.Close()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Errorf("err = %v, want EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read still blocked after Close")
	}
}

func TestPCMValidation(t *testing.T) {
	if _, err := NewPCM(bytes.NewReader(nil), PCMOptions{Rate: 48000, Channels: 3}); err == nil {
		t.Error("3 channels accepted")
	}
	if _, err := NewPCM(bytes.NewReader(nil), PCMOptions{Rate: 100, Channels: 1}); err == nil {
		t.Error("100 Hz accepted")
	}
	if _, err := ParsePCMFormat("mp3"); err == nil {
		t.Error("bad format accepted")
	}
}
