package ws

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"testing"
)

func TestAcceptKey(t *testing.T) { // the example of RFC 6455 section 1.3
	if got := AcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal(got)
	}
}

// serve upgrades one connection and hands it to f (server side: frames unmasked).
func serve(t *testing.T, f func(c net.Conn)) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var key string
		// read the request by hand: header lines up to the blank line
		buf := make([]byte, 0, 1024)
		one := make([]byte, 1)
		for len(buf) < 4 || string(buf[len(buf)-4:]) != "\r\n\r\n" {
			if _, err := c.Read(one); err != nil {
				return
			}
			buf = append(buf, one...)
		}
		req, _ := http.ReadRequest(bufioReader(buf))
		key = req.Header.Get("Sec-WebSocket-Key")
		io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "+AcceptKey(key)+"\r\n\r\n")
		f(c)
	}()
	return ln.Addr().String()
}

func frame(op byte, fin bool, p []byte) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	h := []byte{b0}
	switch n := len(p); {
	case n < 126:
		h = append(h, byte(n))
	case n < 65536:
		h = append(h, 126)
		h = binary.BigEndian.AppendUint16(h, uint16(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	return append(h, p...)
}

func TestReadMessages(t *testing.T) {
	big := make([]byte, 70000)
	for i := range big {
		big[i] = byte(i)
	}
	addr := serve(t, func(c net.Conn) {
		c.Write(frame(0x1, true, []byte("hello")))
		c.Write(frame(0x9, true, []byte("png"))) // a ping in between
		c.Write(frame(0x2, true, big))
		c.Write(frame(0x2, false, []byte{1, 2})) // fragmented
		c.Write(frame(0x0, false, []byte{3}))
		c.Write(frame(0x0, true, []byte{4}))
		c.Write(frame(0x8, true, nil))
		io.Copy(io.Discard, c)
	})
	c, err := Dial(context.Background(), addr, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	typ, m, err := c.Read()
	if err != nil || typ != Text || string(m) != "hello" {
		t.Fatal(typ, m, err)
	}
	typ, m, err = c.Read()
	if err != nil || typ != Binary || len(m) != len(big) || m[69999] != big[69999] {
		t.Fatal(typ, len(m), err)
	}
	typ, m, err = c.Read()
	if err != nil || typ != Binary || string(m) != "\x01\x02\x03\x04" {
		t.Fatal(typ, m, err)
	}
	if _, _, err = c.Read(); err != ErrClosed {
		t.Fatal(err)
	}
}

func TestWriteIsMasked(t *testing.T) {
	got := make(chan []byte, 1)
	addr := serve(t, func(c net.Conn) {
		b := make([]byte, 64)
		n, _ := c.Read(b)
		got <- b[:n]
	})
	c, err := Dial(context.Background(), addr, "/")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.WriteText("abc")
	b := <-got
	if len(b) != 2+4+3 || b[0] != 0x81 || b[1] != 0x83 {
		t.Fatalf("% x", b)
	}
	m := b[2:6]
	if string([]byte{b[6] ^ m[0], b[7] ^ m[1], b[8] ^ m[2]}) != "abc" {
		t.Fatalf("% x", b)
	}
}

func bufioReader(b []byte) *bufio.Reader { return bufio.NewReader(bytes.NewReader(b)) }
