package iq

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"
)

// PCMFormat is the sample encoding of a raw PCM stream.
type PCMFormat int

const (
	S16LE PCMFormat = iota // signed 16-bit little endian (sound cards, arecord, parec)
	F32LE                  // 32-bit float little endian
)

// ParsePCMFormat maps "s16le" / "f32le" to a PCMFormat.
func ParsePCMFormat(s string) (PCMFormat, error) {
	switch s {
	case "s16le", "":
		return S16LE, nil
	case "f32le":
		return F32LE, nil
	}
	return 0, fmt.Errorf("unknown PCM format %q (want s16le or f32le)", s)
}

func (f PCMFormat) bytes() int {
	if f == F32LE {
		return 4
	}
	return 2
}

// PCMOptions describes a raw PCM stream.
type PCMOptions struct {
	Rate     float64
	Channels int // 1 = real audio, 2 = I/Q
	Format   PCMFormat
	CentreHz float64
	SwapIQ   bool
}

// PCMSource turns a raw PCM byte stream (stdin, a sound-card capture, a child process) into a Source. A goroutine
// reads the stream so that Read can be interrupted by Close, and short bursts are absorbed by a queue.
type PCMSource struct {
	opt    PCMOptions
	align  int
	ch     chan []byte
	done   chan struct{}
	once   sync.Once
	cur    []byte // unread bytes of the current chunk
	err    error  // terminal error from the reader goroutine (io.EOF at a clean end)
	closer io.Closer
}

// NewPCM starts reading r. If r is also an io.Closer it is closed by Close.
func NewPCM(r io.Reader, o PCMOptions) (*PCMSource, error) {
	if o.Channels != 1 && o.Channels != 2 {
		return nil, fmt.Errorf("PCM channels must be 1 (real audio) or 2 (I/Q), got %d", o.Channels)
	}
	if o.Rate < 8000 {
		return nil, fmt.Errorf("PCM sample rate %.0f Hz is too low", o.Rate)
	}
	s := &PCMSource{opt: o, align: o.Channels * o.Format.bytes(), ch: make(chan []byte, 256), done: make(chan struct{})}
	if c, ok := r.(io.Closer); ok {
		s.closer = c
	}
	go s.pump(r)
	return s, nil
}

func (s *PCMSource) pump(r io.Reader) {
	defer close(s.ch)
	size := s.align * 2048
	for {
		b := make([]byte, size)
		n, err := r.Read(b)
		if n > 0 {
			select {
			case s.ch <- b[:n]:
			case <-s.done:
				return
			}
		}
		if err != nil {
			s.err = err
			return
		}
	}
}

func (s *PCMSource) SampleRate() float64 { return s.opt.Rate }
func (s *PCMSource) CentreHz() float64   { return s.opt.CentreHz }
func (s *PCMSource) Kind() Kind {
	if s.opt.Channels == 1 {
		return Real
	}
	return IQ
}

// Read returns whatever whole frames are available (at least one), blocking until then.
func (s *PCMSource) Read(dst []complex64) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(s.cur) < s.align {
		var chunk []byte
		var ok bool
		select {
		case chunk, ok = <-s.ch:
		case <-s.done:
			return 0, io.EOF
		}
		if !ok {
			if s.err != nil && s.err != io.EOF && s.err != io.ErrUnexpectedEOF {
				return 0, s.err
			}
			return 0, io.EOF
		}
		s.cur = append(s.cur, chunk...)
	}
	// collect more chunks that are already queued, so blocks are not needlessly small
	for len(s.cur) < len(dst)*s.align {
		select {
		case chunk, ok := <-s.ch:
			if !ok {
				goto convert
			}
			s.cur = append(s.cur, chunk...)
			continue
		default:
		}
		break
	}
convert:
	frames := len(s.cur) / s.align
	if frames > len(dst) {
		frames = len(dst)
	}
	b := s.cur
	for i := 0; i < frames; i++ {
		var a, c float32
		o := i * s.align
		a = s.sample(b[o:])
		if s.opt.Channels == 2 {
			c = s.sample(b[o+s.opt.Format.bytes():])
			if s.opt.SwapIQ {
				a, c = c, a
			}
		}
		dst[i] = complex(a, c)
	}
	s.cur = append(s.cur[:0], s.cur[frames*s.align:]...)
	return frames, nil
}

func (s *PCMSource) sample(b []byte) float32 {
	if s.opt.Format == F32LE {
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	}
	return float32(int16(binary.LittleEndian.Uint16(b))) / 32768
}

// Close stops the source; a blocked Read returns io.EOF.
func (s *PCMSource) Close() error {
	s.once.Do(func() { close(s.done) })
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
