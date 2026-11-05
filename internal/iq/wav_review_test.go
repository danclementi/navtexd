package iq

// Review tests (not part of the project). See the review report.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func reviewWav(t *testing.T, channels, bits, align int, fmtSize uint32, frames int) string {
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, fmtSize)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, uint16(channels))
	b = binary.LittleEndian.AppendUint32(b, 48000)
	b = binary.LittleEndian.AppendUint32(b, 48000*uint32(align))
	b = binary.LittleEndian.AppendUint16(b, uint16(align))
	b = binary.LittleEndian.AppendUint16(b, uint16(bits))
	b = append(b, "data"...)
	n := frames * channels * bits / 8
	b = binary.LittleEndian.AppendUint32(b, uint32(n))
	b = append(b, make([]byte, n)...)
	p := filepath.Join(t.TempDir(), "x_516000HZ.wav")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Finding I1: block_align is taken from the file and only replaced when it is 0. A value smaller than channels*bits/8 makes
// Read index past the end of its buffer (panic). In navtexd the runner recovers and restarts the channel every 3 s for ever;
// cmd/navtex crashes.
func TestReview_WavSmallBlockAlignPanics(t *testing.T) {
	p := reviewWav(t, 2, 16, 1, 16, 100)
	w, err := OpenWav(p, WavOptions{})
	if err != nil {
		t.Skip("refused at open (fixed):", err)
	}
	defer w.Close()
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("Read panicked on block_align=1 for 16-bit stereo: %v", v)
		}
	}()
	dst := make([]complex64, 4096)
	for {
		if _, err := w.Read(dst); err != nil {
			break
		}
	}
}

// Finding I2: the fmt (and ds64) chunk body is allocated with the size the file states (up to 4 GiB) before anything is checked.
// On armv7 a size above 2^31-1 makes make() panic ("len out of range"); on 64-bit it allocates up to 4 GiB for a 50-byte file.
func TestReview_WavHugeFmtChunk(t *testing.T) {
	p := reviewWav(t, 2, 16, 4, 0x7FFFFFF0, 0) // 2 GiB fmt chunk claimed in a 44-byte file
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("OpenWav panicked: %v", v)
		}
	}()
	var before, after runtimeMem
	before.read()
	OpenWav(p, WavOptions{})
	after.read()
	if d := after.total - before.total; d > 100<<20 {
		t.Fatalf("OpenWav of a %d-byte file allocated %d MB", 44, d>>20)
	}
}
