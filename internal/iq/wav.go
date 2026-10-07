package iq

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// WavOptions configures a WavSource.
type WavOptions struct {
	SwapIQ    bool    // stereo only: left = Q, right = I
	CentreHz  float64 // 0 => parse from file name ("..._501500HZ.wav" or "...504kHz...")
	HasCentre bool    // set true to use CentreHz even when it is 0
}

// WavSource streams a WAV file with plain reads (no memory mapping, so large files are fine on Windows).
// Supported: PCM 8/16/24/32-bit, float32/64, WAVE_FORMAT_EXTENSIBLE, RIFF and RF64, and data-chunk sizes
// that are 0, 0xFFFFFFFF or larger than the file (clamped to the bytes present).
// Mono = real audio (Kind Real); stereo = I/Q.
type WavSource struct {
	f        *os.File
	opt      WavOptions
	tag      uint16
	bits     int
	channels int
	align    int
	rate     float64
	centre   float64
	frames   int64 // frames remaining
	buf      []byte
}

var (
	reKHz = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*kHz`)
	reHz  = regexp.MustCompile(`(?i)(\d{4,10})\s*Hz`)
)

// ParseCentre extracts a centre frequency in Hz from a file name, or reports false.
func ParseCentre(name string) (float64, bool) {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if m := reKHz.FindStringSubmatch(name); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v * 1e3, true
	}
	if m := reHz.FindStringSubmatch(name); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v, true
	}
	return 0, false
}

// OpenWav opens path and parses its header.
func OpenWav(path string, opt WavOptions) (*WavSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	w := &WavSource{f: f, opt: opt}
	if err := w.parseHeader(); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case opt.HasCentre || opt.CentreHz != 0:
		w.centre = opt.CentreHz
	default:
		c, ok := ParseCentre(path)
		if !ok && w.channels == 2 {
			f.Close()
			return nil, errors.New("centre frequency not in file name; set it explicitly")
		}
		w.centre = c
	}
	return w, nil
}

func (w *WavSource) parseHeader() error {
	st, err := w.f.Stat()
	if err != nil {
		return err
	}
	fsize := st.Size()
	var hdr [12]byte
	if _, err := io.ReadFull(w.f, hdr[:]); err != nil {
		return err
	}
	if (string(hdr[:4]) != "RIFF" && string(hdr[:4]) != "RF64") || string(hdr[8:12]) != "WAVE" {
		return errors.New("not a RIFF/RF64 WAVE file")
	}
	var haveFmt, haveData bool
	var ds64 uint64
	var dataStart, dataSize int64
	for !haveData {
		var ch [8]byte
		if _, err := io.ReadFull(w.f, ch[:]); err != nil {
			break
		}
		id, size := string(ch[:4]), int64(binary.LittleEndian.Uint32(ch[4:]))
		pos, _ := w.f.Seek(0, io.SeekCurrent)
		switch id {
		case "fmt ":
			body := make([]byte, size)
			if _, err := io.ReadFull(w.f, body); err != nil || size < 16 {
				return errors.New("bad fmt chunk")
			}
			w.tag = binary.LittleEndian.Uint16(body[0:])
			w.channels = int(binary.LittleEndian.Uint16(body[2:]))
			w.rate = float64(binary.LittleEndian.Uint32(body[4:]))
			w.align = int(binary.LittleEndian.Uint16(body[12:]))
			w.bits = int(binary.LittleEndian.Uint16(body[14:]))
			if w.tag == 0xFFFE && size >= 26 {
				w.tag = binary.LittleEndian.Uint16(body[24:])
			}
			haveFmt = true
			w.f.Seek(pos+size+(size&1), io.SeekStart)
		case "ds64":
			body := make([]byte, size)
			if _, err := io.ReadFull(w.f, body); err == nil && size >= 16 {
				ds64 = binary.LittleEndian.Uint64(body[8:])
			}
			w.f.Seek(pos+size+(size&1), io.SeekStart)
		case "data":
			dataStart, dataSize = pos, size
			if (dataSize == 0 || dataSize == 0xFFFFFFFF) && ds64 != 0 {
				dataSize = int64(ds64)
			}
			if dataSize == 0 || dataSize == 0xFFFFFFFF || dataStart+dataSize > fsize {
				dataSize = fsize - dataStart
			}
			haveData = true
		default:
			w.f.Seek(pos+size+(size&1), io.SeekStart)
		}
	}
	if !haveFmt || !haveData {
		return errors.New("WAV header has no fmt/data chunk")
	}
	if w.channels != 1 && w.channels != 2 {
		return fmt.Errorf("expected 1 (real audio) or 2 (I/Q) channels, found %d", w.channels)
	}
	ok := (w.tag == 1 && (w.bits == 8 || w.bits == 16 || w.bits == 24 || w.bits == 32)) ||
		(w.tag == 3 && (w.bits == 32 || w.bits == 64))
	if !ok {
		return fmt.Errorf("unsupported WAV format: tag %d, %d bits", w.tag, w.bits)
	}
	if w.align == 0 {
		w.align = w.channels * w.bits / 8
	}
	w.frames = dataSize / int64(w.align)
	_, err = w.f.Seek(dataStart, io.SeekStart)
	return err
}

func (w *WavSource) SampleRate() float64 { return w.rate }
func (w *WavSource) CentreHz() float64   { return w.centre }
func (w *WavSource) Channels() int       { return w.channels }
func (w *WavSource) Frames() int64       { return w.frames }
func (w *WavSource) Close() error        { return w.f.Close() }

func (w *WavSource) Kind() Kind {
	if w.channels == 1 {
		return Real
	}
	return IQ
}

// sample decodes one value at byte offset o of buf, scaled to about [-1, 1).
func (w *WavSource) sample(b []byte) float32 {
	switch {
	case w.tag == 1 && w.bits == 8:
		return (float32(b[0]) - 128) / 128
	case w.tag == 1 && w.bits == 16:
		return float32(int16(binary.LittleEndian.Uint16(b))) / 32768
	case w.tag == 1 && w.bits == 24:
		v := int32(b[0]) | int32(b[1])<<8 | int32(int8(b[2]))<<16
		return float32(v) / 8388608
	case w.tag == 1 && w.bits == 32:
		return float32(int32(binary.LittleEndian.Uint32(b))) / 2147483648
	case w.tag == 3 && w.bits == 32:
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	default: // float64
		return float32(math.Float64frombits(binary.LittleEndian.Uint64(b)))
	}
}

// Read implements Source.
func (w *WavSource) Read(dst []complex64) (int, error) {
	if w.frames <= 0 {
		return 0, io.EOF
	}
	n := int64(len(dst))
	if n > w.frames {
		n = w.frames
	}
	need := int(n) * w.align
	if cap(w.buf) < need {
		w.buf = make([]byte, need)
	}
	buf := w.buf[:need]
	got, err := io.ReadFull(w.f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	m := got / w.align
	if m == 0 {
		w.frames = 0
		return 0, io.EOF
	}
	w.frames -= int64(m)
	bytesPer := w.bits / 8
	for i := 0; i < m; i++ {
		o := i * w.align
		a := w.sample(buf[o:])
		if w.channels == 1 {
			dst[i] = complex(a, 0)
			continue
		}
		b := w.sample(buf[o+bytesPer:])
		if w.opt.SwapIQ {
			a, b = b, a
		}
		dst[i] = complex(a, b)
	}
	return m, nil
}
