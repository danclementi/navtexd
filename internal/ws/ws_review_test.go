package ws

// Review tests (not part of the project). See the review report.

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// Finding W1: a continuation frame with no message started is accepted and returned with message type 0 (RFC 6455 5.4: the
// connection must be failed). Callers switch on Text/Binary, so the data is silently dropped, but the API returns a type that
// is neither.
func TestReview_ContinuationWithoutStart(t *testing.T) {
	addr := serve(t, func(c net.Conn) {
		c.Write(frame(0x0, true, []byte("orphan")))
		time.Sleep(200 * time.Millisecond)
	})
	c, err := Dial(context.Background(), addr, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	typ, p, err := c.Read()
	if err == nil {
		t.Fatalf("orphan continuation accepted: type %d, %q (want a protocol error)", typ, p)
	}
}

// Finding W2: a new data frame while a fragmented message is open silently discards the fragments received so far
// (RFC 6455 5.4: must fail the connection).
func TestReview_InterleavedDataFrameDropsFragments(t *testing.T) {
	addr := serve(t, func(c net.Conn) {
		c.Write(frame(0x1, false, []byte("AAA")))
		c.Write(frame(0x1, true, []byte("BBB")))
		time.Sleep(200 * time.Millisecond)
	})
	c, err := Dial(context.Background(), addr, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, p, err := c.Read()
	if err == nil {
		t.Fatalf("got %q with no error: the open fragment AAA was dropped silently", p)
	}
}

// Finding W3: control frames are not limited to 125 bytes / FIN (RFC 6455 5.5). A server ping of 16 MiB is accepted,
// buffered and echoed back as a 16 MiB pong.
func TestReview_HugePingEchoed(t *testing.T) {
	big := bytes.Repeat([]byte{'x'}, 1<<20)
	echoed := make(chan int, 1)
	addr := serve(t, func(c net.Conn) {
		c.Write(frame(0x9, true, big))
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var h [2]byte
		if _, err := io.ReadFull(c, h[:]); err != nil {
			echoed <- -1
			return
		}
		echoed <- int(h[0] & 0x0f)
		time.Sleep(200 * time.Millisecond)
	})
	c, err := Dial(context.Background(), addr, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go c.Read()
	if op := <-echoed; op == 0xA {
		t.Fatal("a 1 MiB ping (control frames are limited to 125 bytes) was accepted and answered with a 1 MiB pong")
	}
}

// Finding W4: a masked frame from the server is accepted (RFC 6455 5.1: the client must close the connection).
func TestReview_MaskedServerFrameAccepted(t *testing.T) {
	addr := serve(t, func(c net.Conn) {
		p := []byte("hi")
		mask := []byte{1, 2, 3, 4}
		f := []byte{0x81, 0x80 | byte(len(p))}
		f = append(f, mask...)
		for i, b := range p {
			f = append(f, b^mask[i&3])
		}
		c.Write(f)
		time.Sleep(200 * time.Millisecond)
	})
	c, err := Dial(context.Background(), addr, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, p, err := c.Read(); err == nil {
		t.Fatalf("masked server frame accepted: %q", p)
	}
}
