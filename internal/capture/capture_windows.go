//go:build windows

package capture

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	winmm    = syscall.NewLazyDLL("winmm.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pGetNumDevs   = winmm.NewProc("waveInGetNumDevs")
	pGetDevCaps   = winmm.NewProc("waveInGetDevCapsW")
	pOpen         = winmm.NewProc("waveInOpen")
	pClose        = winmm.NewProc("waveInClose")
	pPrepare      = winmm.NewProc("waveInPrepareHeader")
	pUnprepare    = winmm.NewProc("waveInUnprepareHeader")
	pAddBuffer    = winmm.NewProc("waveInAddBuffer")
	pStart        = winmm.NewProc("waveInStart")
	pStop         = winmm.NewProc("waveInStop")
	pReset        = winmm.NewProc("waveInReset")
	pCreateEvent  = kernel32.NewProc("CreateEventW")
	pCloseHandle  = kernel32.NewProc("CloseHandle")
	pWaitForEvent = kernel32.NewProc("WaitForSingleObject")
)

const (
	waveMapper    = 0xFFFFFFFF
	callbackEvent = 0x00050000
	whdrDone      = 0x1
	formatPCM     = 1
	bufMillis     = 50
	numBuffers    = 16
)

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

type waveInCaps struct {
	Mid, Pid uint16
	Ver      uint32
	Name     [32]uint16
	Formats  uint32
	Channels uint16
	Reserved uint16
}

func mmErr(what string, r uintptr) error {
	if r == 0 {
		return nil
	}
	extra := ""
	switch r {
	case 2:
		extra = " (bad device)"
	case 4:
		extra = " (already in use by another program)"
	case 32:
		extra = " (the device does not accept this sample rate / channel count; try input.sample_rate_hz = 48000 or 44100)"
	}
	return fmt.Errorf("%s failed: MMSYSERR %d%s", what, r, extra)
}

func devices() ([]string, error) {
	n, _, _ := pGetNumDevs.Call()
	var names []string
	for i := uintptr(0); i < n; i++ {
		var c waveInCaps
		r, _, _ := pGetDevCaps.Call(i, uintptr(unsafe.Pointer(&c)), unsafe.Sizeof(c))
		if r != 0 {
			return nil, mmErr("waveInGetDevCaps", r)
		}
		names = append(names, syscall.UTF16ToString(c.Name[:]))
	}
	return names, nil
}

func list() ([]Device, error) {
	names, err := devices()
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, n := range names {
		out = append(out, Device{Name: n, Note: "Windows truncates names to 31 characters; a part of the name is enough"})
	}
	return out, nil
}

type winCap struct {
	mu      sync.Mutex
	h       uintptr
	ev      uintptr
	bufs    [numBuffers][]byte
	hdrs    [numBuffers]*waveHdr
	next    int
	cur     []byte
	closed  bool
	started bool
}

func open(name string, rate float64, channels int) (ReadCloser, error) {
	id := uintptr(waveMapper)
	if name != "" && !strings.EqualFold(name, "default") {
		names, err := devices()
		if err != nil {
			return nil, err
		}
		found := -1
		lname := strings.ToLower(name)
		for i, n := range names {
			if strings.Contains(strings.ToLower(n), lname) {
				found = i
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("no audio input matches %q; available: %s", name, strings.Join(names, "; "))
		}
		id = uintptr(found)
	}
	c := &winCap{}
	ev, _, e := pCreateEvent.Call(0, 0, 0, 0)
	if ev == 0 {
		return nil, fmt.Errorf("CreateEvent: %v", e)
	}
	c.ev = ev
	f := waveFormatEx{FormatTag: formatPCM, Channels: uint16(channels), SamplesPerSec: uint32(rate), BitsPerSample: 16}
	f.BlockAlign = f.Channels * 2
	f.AvgBytesPerSec = f.SamplesPerSec * uint32(f.BlockAlign)
	r, _, _ := pOpen.Call(uintptr(unsafe.Pointer(&c.h)), id, uintptr(unsafe.Pointer(&f)), ev, 0, callbackEvent)
	if err := mmErr("waveInOpen", r); err != nil {
		pCloseHandle.Call(ev)
		return nil, err
	}
	size := int(rate) * int(f.BlockAlign) * bufMillis / 1000
	size -= size % int(f.BlockAlign)
	for i := range c.bufs {
		c.bufs[i] = make([]byte, size)
		h := &waveHdr{Data: uintptr(unsafe.Pointer(&c.bufs[i][0])), BufferLength: uint32(size)}
		c.hdrs[i] = h
		if r, _, _ := pPrepare.Call(c.h, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h)); r != 0 {
			c.Close()
			return nil, mmErr("waveInPrepareHeader", r)
		}
		if r, _, _ := pAddBuffer.Call(c.h, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h)); r != 0 {
			c.Close()
			return nil, mmErr("waveInAddBuffer", r)
		}
	}
	if r, _, _ := pStart.Call(c.h); r != 0 {
		c.Close()
		return nil, mmErr("waveInStart", r)
	}
	c.started = true
	return c, nil
}

// Read returns captured bytes, blocking until a buffer has been filled.
func (c *winCap) Read(p []byte) (int, error) {
	for len(c.cur) == 0 {
		if c.isClosed() {
			return 0, io.EOF
		}
		h := c.hdrs[c.next]
		if h.Flags&whdrDone == 0 {
			pWaitForEvent.Call(c.ev, 100)
			continue
		}
		n := int(h.BytesRecorded)
		c.cur = append(c.cur[:0], c.bufs[c.next][:n]...)
		h.BytesRecorded = 0
		h.Flags &^= whdrDone
		pAddBuffer.Call(c.h, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
		c.next = (c.next + 1) % numBuffers
	}
	n := copy(p, c.cur)
	c.cur = c.cur[n:]
	return n, nil
}

func (c *winCap) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *winCap) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	if c.h == 0 {
		return errors.New("not open")
	}
	pStop.Call(c.h)
	pReset.Call(c.h)
	for _, h := range c.hdrs {
		if h != nil {
			pUnprepare.Call(c.h, uintptr(unsafe.Pointer(h)), unsafe.Sizeof(*h))
		}
	}
	pClose.Call(c.h)
	pCloseHandle.Call(c.ev)
	return nil
}
