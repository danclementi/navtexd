package yand

// Review tests (not part of the project). See the review report.

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countingReaderAt struct {
	r     *bytes.Reader
	bytes atomic.Int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.bytes.Add(int64(n))
	return n, err
}

// reviewPage makes a raw 4096-byte page with an ABSP header of the given type.
func reviewPage(typ int) []byte {
	p := make([]byte, pageSize)
	copy(p[hdrAt:], "ABSP")
	binary.LittleEndian.PutUint16(p[hdrAt+8:], uint16(typ))
	return p
}

// reviewFile builds: page 0 magic; page 1 a blob page whose single text claims to be `blobLen` long (so it is "chained" over
// all following pages); then nRowPages pages of rows, all pointing at the text of page 1 with a time that does not match it.
func reviewFile(nRowPages int, blobLen uint32) []byte {
	var out bytes.Buffer
	first := make([]byte, pageSize)
	copy(first, Magic)
	binary.LittleEndian.PutUint16(first[26:], pageSize)
	out.Write(first)

	bp := reviewPage(typeBlobs)
	// data offset 380 is page offset 420; header k=8: length, 0, length, 0
	o := hdrAt + hdrLen + 8
	binary.LittleEndian.PutUint32(bp[o:], blobLen)
	binary.LittleEndian.PutUint32(bp[o+8:], blobLen)
	out.Write(bp)

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	days := int(t0.Unix()/86400) + 719163
	for pg := 0; pg < nRowPages; pg++ {
		p := reviewPage(typeRows)
		d := make([]byte, dataLen)
		for o := 0; o+recLen <= dataLen; o += recLen {
			rec := d[o : o+recLen]
			rec[0] = 0xFC
			binary.LittleEndian.PutUint32(rec[1:], uint32(days))
			binary.LittleEndian.PutUint32(rec[5:], 1000)
			binary.LittleEndian.PutUint32(rec[9:], 518)
			binary.LittleEndian.PutUint64(rec[13:], math.Float64bits(10))
			binary.LittleEndian.PutUint32(rec[107:], 1) // text in page 1
		}
		copy(p, d[:hdrAt])
		copy(p[hdrAt+hdrLen:], d[hdrAt:])
		out.Write(p)
	}
	return out.Bytes()
}

// Finding Y1: every record whose text is a long chained blob re-reads (and re-allocates) the whole chain, so the work is
// rows × pages: quadratic in the file size. A 400 KB file causes > 300 MB of reads here; a 20 MB file (well under the 1 GiB
// upload limit) would need ~ 1 TB.
func TestReview_ABSQuadraticReread(t *testing.T) {
	const rowPages = 100
	data := reviewFile(rowPages, 1<<27) // 128 MiB claimed length (< 1<<28 accepted by parse)
	cr := &countingReaderAt{r: bytes.NewReader(data)}
	recs, _, err := ReadABS(cr, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	ratio := float64(cr.bytes.Load()) / float64(len(data))
	t.Logf("file %d bytes, %d records, read %d bytes (%.0fx the file)", len(data), len(recs), cr.bytes.Load(), ratio)
	if ratio > 20 {
		t.Fatalf("ReadABS read %.0fx the size of the file: work is O(records x pages)", ratio)
	}
}

// Fuzz target: ReadABS must not panic or loop on arbitrary data after a valid first page.
func FuzzReview_ReadABS(f *testing.F) {
	f.Add(reviewFile(1, 5000))
	f.Add(reviewFile(1, 10))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64*pageSize {
			return
		}
		first := make([]byte, pageSize)
		copy(first, Magic)
		binary.LittleEndian.PutUint16(first[26:], pageSize)
		data := append(first, b...)
		ReadABS(bytes.NewReader(data), int64(len(data)))
	})
}

func FuzzReview_ReadReport(f *testing.F) {
	f.Add("20261002 0815 518 $04B ZBR Bermuda,BER   1286 [antenna]\n2026-10-02 08:15:00 50> ZCZC BA12\n2026-10-02 08:15:03> NNNN\n---\n")
	f.Fuzz(func(t *testing.T, s string) {
		ReadReport(strings.NewReader(s))
	})
}
