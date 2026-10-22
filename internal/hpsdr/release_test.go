package hpsdr

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// A radio that says it is busy until it gets the stop command is released; one that is free is left alone.
func TestRelease(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var busy atomic.Bool
	busy.Store(true)
	var stops atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			switch {
			case n >= 3 && buf[2] == 2: // discovery
				r := make([]byte, 60)
				copy(r, []byte{0xef, 0xfe, 2, 1, 2, 3, 4, 5, 6, 25, 1})
				if busy.Load() {
					r[2] = 3
				}
				conn.WriteToUDP(r, from)
			case n == 64 && buf[2] == 4 && buf[3] == 0: // stop
				stops.Add(1)
				busy.Store(false)
			}
		}
	}()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	ctx := context.Background()
	st, err := Release(ctx, "127.0.0.1", port)
	if err != nil || st.Busy || stops.Load() == 0 {
		t.Fatalf("release: %+v %v stops=%d", st, err, stops.Load())
	}
	n := stops.Load()
	if _, err := Release(ctx, "127.0.0.1", port); err == nil || stops.Load() != n {
		t.Fatalf("a free radio must be left alone: %v stops=%d", err, stops.Load())
	}
	time.Sleep(10 * time.Millisecond)
}
