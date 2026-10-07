// Package clip keeps the most recent input samples in a ring buffer and writes them to a WAV file on request, so that
// a stretch of signal can be analysed or replayed later with different settings.
package clip

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"navtex/internal/iq"
)

// Meta is written beside each clip (same name, .json) and read back when the clip is replayed.
type Meta struct {
	StartUTC   string          `json:"start_utc"` // time of the first sample, UTC, yyyy-mm-dd hh:mm:ss.mmm
	PressedUTC string          `json:"pressed_utc"`
	SampleRate float64         `json:"sample_rate_hz"`
	Kind       string          `json:"kind"` // real or iq
	CentreHz   float64         `json:"centre_hz"`
	ChannelHz  float64         `json:"channel_hz"`
	CarrierHz  float64         `json:"carrier_audio_hz,omitempty"` // carrier position at the time of the request, if known
	Autotune   bool            `json:"autotune"`
	PreS       float64         `json:"before_s"`
	PostS      float64         `json:"after_s"`
	Seconds    float64         `json:"seconds"`
	Frames     int             `json:"frames"`
	Software   string          `json:"software,omitempty"`
	Settings   json.RawMessage `json:"settings,omitempty"` // the receiver-related options in force
}

// TimeFormat is the layout of Meta.StartUTC.
const TimeFormat = "2006-01-02 15:04:05.000"

// Ring holds the last pre+post seconds of input.
type Ring struct {
	mu        sync.Mutex
	rate      float64
	kind      iq.Kind
	ch        int
	buf       []float32 // frames * ch
	capFr     int
	total     int64     // frames written since the start
	last      time.Time // wall-clock time of the newest frame
	pending   bool
	stopAt    int64 // frame count at which the pending request completes
	pressed   time.Time
	pre, post float64
}

// MaxBytes limits the memory used by the ring.
const MaxBytes = 256 << 20

// New creates a ring for the given source. It reports the capacity actually obtained (the request is cut to MaxBytes).
func New(rate float64, kind iq.Kind, pre, post float64) (*Ring, float64) {
	ch := 1
	if kind == iq.IQ {
		ch = 2
	}
	fr := int((pre + post) * rate)
	if limit := MaxBytes / (4 * ch); fr > limit {
		fr = limit
	}
	fr = max(fr, 1)
	return &Ring{rate: rate, kind: kind, ch: ch, buf: make([]float32, fr*ch), capFr: fr, pre: pre, post: post}, float64(fr) / rate
}

// Write appends samples; now is the wall-clock time of the newest of them. It reports whether a requested clip is
// now complete (call Take).
func (r *Ring) Write(s []complex64, now time.Time) (done bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pos := int(r.total % int64(r.capFr))
	for _, v := range s {
		if r.ch == 1 {
			r.buf[pos] = real(v)
		} else {
			r.buf[2*pos], r.buf[2*pos+1] = real(v), imag(v)
		}
		if pos++; pos == r.capFr {
			pos = 0
		}
	}
	r.total += int64(len(s))
	r.last = now
	return r.pending && r.total >= r.stopAt
}

// Request asks for a clip that ends post seconds from now. It fails when one is already waiting.
func (r *Ring) Request() (wait float64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending {
		return 0, fmt.Errorf("a clip is already being recorded")
	}
	if r.total == 0 {
		return 0, fmt.Errorf("no input yet")
	}
	r.pending, r.pressed = true, r.last // the time line of the input (a replayed file has its own)
	r.stopAt = r.total + int64(r.post*r.rate)
	if r.post*r.rate > float64(r.capFr) {
		r.stopAt = r.total + int64(r.capFr)
	}
	return r.post, nil
}

// Pending reports whether a request is waiting for its post-roll.
func (r *Ring) Pending() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.pending }

// Take copies the finished clip out of the ring and clears the request.
func (r *Ring) Take() (frames []float32, m Meta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = false
	n := int(min(int64(r.capFr), r.total))
	frames = make([]float32, n*r.ch)
	start := int((r.total - int64(n)) % int64(r.capFr))
	k := copy(frames, r.buf[start*r.ch:])
	if k < len(frames) {
		copy(frames[k:], r.buf)
	}
	m = Meta{SampleRate: r.rate, Kind: "real", Frames: n, Seconds: float64(n) / r.rate, PreS: r.pre, PostS: r.post}
	if r.ch == 2 {
		m.Kind = "iq"
	}
	first := r.last.Add(-time.Duration(float64(n) / r.rate * float64(time.Second)))
	m.StartUTC = first.UTC().Format(TimeFormat)
	m.PressedUTC = r.pressed.UTC().Format(TimeFormat)
	return frames, m
}

// Name returns the base file name (without extension) for a clip.
func Name(m Meta) string {
	t, _ := time.Parse(TimeFormat, m.StartUTC)
	return fmt.Sprintf("clip_%s_%.0fHz", t.UTC().Format("20060102T150405Z"), m.CentreHz)
}

// Write stores the clip as <dir>/<name>.wav (32-bit float, mono for real audio, stereo I/Q) and <name>.json.
func Write(dir string, frames []float32, m Meta) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := Name(m)
	ch := 1
	if m.Kind == "iq" {
		ch = 2
	}
	data := len(frames) * 4
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+data))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 3) // IEEE float
	binary.LittleEndian.PutUint16(hdr[22:], uint16(ch))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(m.SampleRate+0.5))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(m.SampleRate+0.5)*uint32(4*ch))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(4*ch))
	binary.LittleEndian.PutUint16(hdr[34:], 32)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(data))
	body := make([]byte, data)
	for i, v := range frames {
		binary.LittleEndian.PutUint32(body[4*i:], math.Float32bits(v))
	}
	wav := filepath.Join(dir, base+".wav")
	if err := os.WriteFile(wav+".tmp", append(hdr, body...), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(wav+".tmp", wav); err != nil {
		return "", err
	}
	js, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, base+".json"), append(js, '\n'), 0o644); err != nil {
		return base + ".wav", err
	}
	return base + ".wav", nil
}

// ReadMeta reads the sidecar of a WAV file, if there is one.
func ReadMeta(wavPath string) (Meta, bool) {
	b, err := os.ReadFile(wavPath[:len(wavPath)-len(filepath.Ext(wavPath))] + ".json")
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if json.Unmarshal(b, &m) != nil || m.StartUTC == "" || m.SampleRate <= 0 {
		return Meta{}, false
	}
	return m, true
}
