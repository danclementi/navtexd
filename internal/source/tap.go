package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"navtex/internal/iq"
)

// A Tap lets several channels decode the same input stream: the channel that owns the input publishes every block it
// reads, and each channel that shares the input subscribes and gets its own copy (a wide I/Q stream carrying the 490 and
// 518 kHz carriers, say, with one decoder per carrier).
type Tap struct {
	Rate    float64
	Kind    iq.Kind
	Centre  float64
	Start   time.Time
	Live    bool
	Info    string
	mu      sync.Mutex
	subs    map[*Sub]struct{}
	closed  bool
	done    chan struct{}
	Dropped atomic.Uint64 // blocks a subscriber was too slow for (live or paced inputs only)
}

// NewTap describes the stream that the owner's input delivers.
func NewTap(o *Opened) *Tap {
	return &Tap{Rate: o.SampleRate(), Kind: o.Kind(), Centre: o.CentreHz(), Start: o.Start, Live: o.Live, Info: o.Info,
		subs: map[*Sub]struct{}{}, done: make(chan struct{})}
}

// Subscribers is the number of channels now taking samples from the tap.
func (t *Tap) Subscribers() int { t.mu.Lock(); defer t.mu.Unlock(); return len(t.subs) }

// WaitSubscribers waits until n channels have subscribed, for at most d (the channels of a starting program open at the same
// time, and a recording must not be read before all of them are ready).
func (t *Tap) WaitSubscribers(ctx context.Context, n int, d time.Duration) {
	dl := time.NewTimer(d)
	defer dl.Stop()
	for t.Subscribers() < n {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-dl.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Publish gives a block to every subscriber. With block set (a recording read as fast as possible) it waits for slow
// subscribers; otherwise (a live or paced input) a block that a subscriber has no room for is dropped and counted.
func (t *Tap) Publish(ctx context.Context, b []complex64, block bool) {
	t.mu.Lock()
	subs := make([]*Sub, 0, len(t.subs))
	for s := range t.subs {
		subs = append(subs, s)
	}
	t.mu.Unlock()
	for _, s := range subs {
		cp := append([]complex64(nil), b...)
		if block {
			select {
			case s.ch <- cp:
			case <-s.done:
			case <-ctx.Done():
				return
			}
			continue
		}
		select {
		case s.ch <- cp:
		default:
			t.Dropped.Add(1)
		}
	}
}

// Close ends the stream: every subscriber reads io.EOF after what it has.
func (t *Tap) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.done)
	for s := range t.subs {
		close(s.ch)
	}
	t.subs = map[*Sub]struct{}{}
	t.mu.Unlock()
}

// Subscribe adds a channel to the tap.
func (t *Tap) Subscribe() (*Sub, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errors.New("the channel that has the input is not running")
	}
	s := &Sub{t: t, ch: make(chan []complex64, 256), done: make(chan struct{})}
	t.subs[s] = struct{}{}
	return s, nil
}

// Sub is one channel's view of a shared input. It implements iq.Source.
type Sub struct {
	t    *Tap
	ch   chan []complex64
	done chan struct{}
	once sync.Once
	cur  []complex64
}

func (s *Sub) SampleRate() float64 { return s.t.Rate }
func (s *Sub) CentreHz() float64   { return s.t.Centre }
func (s *Sub) Kind() iq.Kind       { return s.t.Kind }

// Read returns the next samples; io.EOF when the input has ended or the subscription was closed.
func (s *Sub) Read(dst []complex64) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(s.cur) == 0 {
		select {
		case b, ok := <-s.ch:
			if !ok {
				return 0, io.EOF
			}
			s.cur = b
		case <-s.done:
			return 0, io.EOF
		}
	}
	n := copy(dst, s.cur)
	s.cur = s.cur[n:]
	return n, nil
}

// Close leaves the tap.
func (s *Sub) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.t.mu.Lock()
		if _, ok := s.t.subs[s]; ok {
			delete(s.t.subs, s)
			// the channel is not closed here: Publish may be sending on it; Read stops at done
		}
		s.t.mu.Unlock()
	})
	return nil
}

// Shared opens the input of a channel that takes its samples from another channel's input.
func Shared(t *Tap, owner string, chHz float64) (*Opened, error) {
	sub, err := t.Subscribe()
	if err != nil {
		return nil, err
	}
	return &Opened{Source: sub, Start: t.Start, Live: t.Live, Close: sub.Close, Shared: true,
		Info: fmt.Sprintf("same input as %s: %s %.0f Hz, channel %.1f kHz, receiver LO %.1f kHz (carrier expected near %.0f Hz)",
			owner, map[bool]string{true: "audio", false: "I/Q"}[t.Kind == iq.Real], t.Rate, chHz/1e3, t.Centre/1e3, chHz-t.Centre)}, nil
}
