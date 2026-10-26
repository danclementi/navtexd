// Package ws is a small WebSocket client (RFC 6455) for plain ws:// connections: enough to talk to SDRconnect's API.
// It handles text and binary messages (also fragmented ones), answers pings and reports a close.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Message types.
const (
	Text   = 1
	Binary = 2
)

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// MaxMessage is the largest message accepted; a longer one is an error.
const MaxMessage = 16 << 20

// ErrClosed is returned by Read after the server closed the connection.
var ErrClosed = errors.New("websocket: closed by the server")

// Conn is a client connection. Read must be called from one goroutine; Write may be called from several.
type Conn struct {
	c   net.Conn
	br  *bufio.Reader
	wmu sync.Mutex
}

// AcceptKey is the Sec-WebSocket-Accept value for a Sec-WebSocket-Key.
func AcceptKey(key string) string {
	h := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h[:])
}

// Dial connects to addr (host:port) and upgrades the connection; path is the request path ("/" if empty).
func Dial(ctx context.Context, addr, path string) (*Conn, error) {
	if path == "" {
		path = "/"
	}
	var d net.Dialer
	d.Timeout = 5 * time.Second
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	rand.Read(kb)
	key := base64.StdEncoding.EncodeToString(kb)
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, addr, key)
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(nc, req); err != nil {
		nc.Close()
		return nil, err
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("no WebSocket answer from %s: %w", addr, err)
	}
	if resp.StatusCode != 101 || !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || resp.Header.Get("Sec-WebSocket-Accept") != AcceptKey(key) {
		nc.Close()
		return nil, fmt.Errorf("%s did not accept the WebSocket upgrade (HTTP %d)", addr, resp.StatusCode)
	}
	nc.SetDeadline(time.Time{})
	return &Conn{c: nc, br: br}, nil
}

// Close closes the connection (after a best-effort close frame).
func (c *Conn) Close() error {
	c.writeFrame(0x8, nil)
	return c.c.Close()
}

// SetReadDeadline sets the deadline of Read.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// WriteText sends a text message.
func (c *Conn) WriteText(s string) error { return c.writeFrame(0x1, []byte(s)) }

func (c *Conn) writeFrame(op byte, p []byte) error {
	hdr := []byte{0x80 | op, 0x80}
	switch n := len(p); {
	case n < 126:
		hdr[1] |= byte(n)
	case n < 65536:
		hdr[1] |= 126
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr[1] |= 127
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	body := make([]byte, len(p))
	for i, b := range p {
		body[i] = b ^ mask[i&3]
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.c.Write(append(hdr, body...))
	return err
}

// Read returns the next text or binary message. Pings are answered and pongs ignored. The returned slice is the caller's.
func (c *Conn) Read() (typ int, msg []byte, err error) {
	var buf []byte
	first := 0
	for {
		var h [2]byte
		if _, err = io.ReadFull(c.br, h[:]); err != nil {
			return 0, nil, err
		}
		fin, op := h[0]&0x80 != 0, int(h[0]&0x0f)
		masked := h[1]&0x80 != 0
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err = io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err = io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > MaxMessage || uint64(len(buf))+n > MaxMessage {
			return 0, nil, fmt.Errorf("websocket: message longer than %d bytes", MaxMessage)
		}
		var mask [4]byte
		if masked {
			if _, err = io.ReadFull(c.br, mask[:]); err != nil {
				return 0, nil, err
			}
		}
		p := make([]byte, n)
		if _, err = io.ReadFull(c.br, p); err != nil {
			return 0, nil, err
		}
		if masked {
			for i := range p {
				p[i] ^= mask[i&3]
			}
		}
		switch op {
		case 0x8:
			c.writeFrame(0x8, nil)
			return 0, nil, ErrClosed
		case 0x9:
			c.writeFrame(0xA, p)
			continue
		case 0xA:
			continue
		case 0x1, 0x2:
			first = op
			buf = p
		case 0x0:
			buf = append(buf, p...)
		default:
			return 0, nil, fmt.Errorf("websocket: unknown frame type %d", op)
		}
		if fin {
			return first, buf, nil
		}
	}
}
