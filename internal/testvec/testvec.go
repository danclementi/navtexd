// Package testvec loads the golden vectors kept in testdata/ (not part of this repository). For tests only.
package testvec

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Path returns the path of a file in the repo's testdata directory, or skips the test if it has not been generated.
func Path(t testing.TB, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s not found in testdata/ (golden vectors are not distributed)", name)
	}
	return p
}

// JSON decodes a testdata JSON file into v.
func JSON(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(Path(t, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// C64 reads a file of little-endian interleaved float32 (I, Q) pairs.
func C64(t testing.TB, name string) []complex64 {
	t.Helper()
	b, err := os.ReadFile(Path(t, name))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]complex64, len(b)/8)
	for i := range out {
		re := math.Float32frombits(binary.LittleEndian.Uint32(b[8*i:]))
		im := math.Float32frombits(binary.LittleEndian.Uint32(b[8*i+4:]))
		out[i] = complex(re, im)
	}
	return out
}

// F32 reads a file of little-endian float32 values.
func F32(t testing.TB, name string) []float32 {
	t.Helper()
	b, err := os.ReadFile(Path(t, name))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

// Bytes reads a raw file (e.g. the .u8 lock masks).
func Bytes(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(Path(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
