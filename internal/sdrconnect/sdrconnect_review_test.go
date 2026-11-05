package sdrconnect

// Review tests (not part of the project). See the review report.

import (
	"context"
	"testing"
	"time"
)

// Finding S1: nothing ever sets a read deadline on the WebSocket, so a server that stops sending but keeps the TCP connection
// open (SDRconnect hung, its PC asleep, a Wi-Fi link gone without a RST) leaves reader() blocked in ws.Conn.Read forever:
// no error, no reconnect, Read() of the source blocks for ever. The fake stops its I/Q stream after 400 frames (~6 s) and
// keeps the connection open; nothing happens after that.
func TestReview_SilentServerNeverDetected(t *testing.T) {
	old := retryEvery
	retryEvery = 200 * time.Millisecond
	t.Cleanup(func() { retryEvery = old })
	f := newFake(t, 0)
	s, err := Open(context.Background(), opts(f.addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Cleanup(func() { // the reconnect goroutine reads retryEvery: wait until it is over before the variable is restored
		for i := 0; i < 500; i++ {
			s.mu.Lock()
			r := s.reconnecting
			s.mu.Unlock()
			if !r {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	f.mu.Lock()
	first := f.conn
	f.mu.Unlock()

	got := make(chan error, 1)
	go func() {
		buf := make([]complex64, 4096)
		for {
			if _, err := s.Read(buf); err != nil {
				got <- err
				return
			}
		}
	}()
	// the fake streams for about 6 s, then goes silent with the connection open; give the client 20 s more
	deadline := time.After(26 * time.Second)
	tk := time.NewTicker(200 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case err := <-got:
			t.Logf("Read ended: %v (detected)", err)
			return
		case <-tk.C:
			f.mu.Lock()
			c := f.conn
			f.mu.Unlock()
			if c != first {
				t.Log("client reconnected (detected)")
				return
			}
		case <-deadline:
			t.Fatal("server silent with the TCP connection open: no error, no reconnect, Read blocks forever (no read deadline / keep-alive)")
		}
	}
}
