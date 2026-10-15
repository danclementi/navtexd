package server

import (
	"testing"
	"time"
)

// A viewer's spectrum rate limit is per decoding channel: frames of the second channel must not be dropped just because
// a frame of the first channel went out a moment before.
func TestSpectrumLimitPerChannel(t *testing.T) {
	h := NewHub()
	c := h.add(Viewer, time.Hour) // nothing but the first frame of each channel gets through
	for i := 0; i < 5; i++ {
		h.Publish("spectrum", spectrumMsg{Rcv: 0}, true, false)
		h.Publish("spectrum", spectrumMsg{Rcv: 1}, true, false)
	}
	if n := len(c.ch); n != 2 {
		t.Fatalf("viewer got %d spectrum frames, want 2 (one per channel)", n)
	}
	h.Publish("meters", meterMsg{}, true, false) // other events are not limited
	if n := len(c.ch); n != 3 {
		t.Fatalf("meters were limited: %d events queued", n)
	}
}
