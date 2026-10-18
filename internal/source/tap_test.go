package source

import (
	"context"
	"io"
	"testing"

	"navtex/internal/iq"
)

type stub struct{}

func (stub) Read([]complex64) (int, error) { return 0, io.EOF }
func (stub) SampleRate() float64           { return 48000 }
func (stub) CentreHz() float64             { return 500000 }
func (stub) Kind() iq.Kind                 { return iq.IQ }

func TestTapFanOut(t *testing.T) {
	o := &Opened{Source: stub{}, Close: func() error { return nil }}
	tap := NewTap(o)
	a, err := Shared(tap, "channel 1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Shared(tap, "channel 1")
	if a.CentreHz() != 500000 || a.SampleRate() != 48000 || !a.Shared {
		t.Fatalf("bad stream description %v %v", a.CentreHz(), a.SampleRate())
	}
	go func() {
		for i := 0; i < 10; i++ {
			blk := make([]complex64, 100)
			blk[0] = complex(float32(i), 0)
			tap.Publish(context.Background(), blk, true)
		}
		tap.Close()
	}()
	for _, s := range []*Opened{a, b} {
		s := s
		n := 0
		buf := make([]complex64, 33)
		for {
			k, err := s.Read(buf)
			n += k
			if err == io.EOF {
				break
			}
		}
		if n != 1000 {
			t.Errorf("subscriber got %d samples, want 1000", n)
		}
	}
	if _, err := Shared(tap, "x"); err == nil {
		t.Error("subscribing to a closed tap should fail")
	}
}
