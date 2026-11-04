package yand

import (
	"bytes"
	"encoding/binary"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A YaND database made here the way the real ones are laid out (see the package comment), to test the reader on things the real
// files showed: texts that share a page, a text that goes over three pages, texts that begin after the end of such a text, a
// header that begins at the very end of a page, and a record whose text is gone.

type row struct {
	t            time.Time
	khz          int
	sq           float64
	id, typ, num string
	area         int
	station      string
	page, idx    int // where the text is
	text         string
}

type builder struct {
	pages [][]byte // the data of each page (4056 bytes)
}

func (b *builder) newPage() int {
	b.pages = append(b.pages, make([]byte, dataLen))
	return len(b.pages) - 1
}

func hdr(n int, typ int) []byte { // a text header: its length twice
	h := make([]byte, 0, 22)
	h = append(h, 1, 0, 0, 0, 0, 0)
	h = binary.LittleEndian.AppendUint32(h, uint32(n))
	h = binary.LittleEndian.AppendUint32(h, 0)
	h = binary.LittleEndian.AppendUint32(h, uint32(n))
	h = binary.LittleEndian.AppendUint32(h, 0)
	return h
}

// put writes texts as a page group starting at stream 380 of a new page; it returns the page and the numbers of the texts.
func (b *builder) put(texts ...string) (int, []int) {
	pg := b.newPage()
	var idx []int
	at := hdrAt
	cur := pg
	for i, t := range texts {
		idx = append(idx, i)
		h := hdr(len(t), 0)
		switch {
		case i == 0 && len(t) <= dataLen-hdrAt-24: // first text of the page: header of 24 bytes (count, first number)
			binary.LittleEndian.PutUint16(b.pages[pg][at:], uint16(len(texts)))
			copy(b.pages[pg][at+8:], h[6:])
			copy(b.pages[pg][at+24:], t)
			at += 24 + len(t)
		case i == 0: // longer than the page: header, the last page, 6 more bytes
			need := len(t) - (dataLen - hdrAt - 32)
			last := pg + 1 + (need-1)/dataLen
			copy(b.pages[pg][at:], h)
			binary.LittleEndian.PutUint32(b.pages[pg][at+22:], uint32(last))
			room := dataLen - (at + 32)
			copy(b.pages[pg][at+32:], t[:room])
			rest := t[room:]
			for p := pg + 1; len(rest) > 0; p++ {
				for len(b.pages) <= p {
					b.newPage()
				}
				n := copy(b.pages[p], rest)
				rest = rest[n:]
				cur = p
				at = n
			}
			// the texts after it go in the tail of the last page
		default:
			whole := append(append([]byte(nil), h...), t...)
			// goes on over the end of the page: the rest in the following pages
			for len(whole) > 0 {
				n := copy(b.pages[cur][at:], whole)
				whole = whole[n:]
				at += n
				if len(whole) > 0 {
					cur++
					for len(b.pages) <= cur {
						b.newPage()
					}
					at = 0
				}
			}
		}
	}
	return pg, idx
}

func (b *builder) file(rows []row) []byte {
	// page 0: the magic and the page size; then the pages of rows; the pages of texts were made by put (numbers shift by 2)
	var out bytes.Buffer
	mk := func(d []byte, typ int) {
		p := make([]byte, pageSize)
		copy(p, d[:hdrAt])
		copy(p[hdrAt:], "ABSP")
		binary.LittleEndian.PutUint16(p[hdrAt+8:], uint16(typ))
		copy(p[hdrAt+hdrLen:], d[hdrAt:])
		out.Write(p)
	}
	first := make([]byte, pageSize)
	copy(first, Magic)
	binary.LittleEndian.PutUint16(first[26:], pageSize)
	copy(first[hdrAt:], "ABSP")
	out.Write(first)
	rd := make([]byte, dataLen)
	for i, r := range rows {
		o := i * recLen
		rec := rd[o : o+recLen]
		rec[0] = 0xFC
		days := int(r.t.Unix()/86400) + 719163 // days since 0001-01-01, counted from 1
		binary.LittleEndian.PutUint32(rec[1:], uint32(days))
		binary.LittleEndian.PutUint32(rec[5:], uint32(r.t.Hour()*3600000+r.t.Minute()*60000+r.t.Second()*1000))
		binary.LittleEndian.PutUint32(rec[9:], uint32(r.khz))
		binary.LittleEndian.PutUint64(rec[13:], math.Float64bits(r.sq))
		copy(rec[21:], r.id)
		copy(rec[23:], r.typ)
		copy(rec[25:], r.num)
		rec[30] = byte(r.area)
		copy(rec[31:], r.station)
		binary.LittleEndian.PutUint32(rec[107:], uint32(r.page+2)) // the texts are after the page of rows
		binary.LittleEndian.PutUint16(rec[111:], uint16(r.idx))
	}
	mk(rd, typeRows)
	for _, d := range b.pages {
		mk(d, typeBlobs)
	}
	return out.Bytes()
}

func line(t time.Time, q int, s string) string {
	if q > 0 {
		return t.Format("2006-01-02 15:04:05") + " " + itoa(q) + "> " + s + "\r\n"
	}
	return t.Format("2006-01-02 15:04:05") + "> " + s + "\r\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

func msg(t time.Time, lines ...string) string {
	var sb strings.Builder
	for i, l := range lines {
		sb.WriteString(line(t.Add(time.Duration(i)*3*time.Second), 50+i, l))
	}
	return sb.String()
}

func TestReadABS(t *testing.T) {
	t0 := time.Date(2026, 1, 18, 8, 52, 10, 0, time.UTC)
	long := []string{"ZCZC BE95"}
	for i := 0; i < 200; i++ {
		long = append(long, strings.Repeat("WINDS 10 TO 15 KT ", 3)+"X")
	}
	long = append(long, "NNNN")
	txtLong := msg(t0, long...)
	if len(txtLong) < 2*dataLen {
		t.Fatalf("test text too short: %d", len(txtLong))
	}
	a := msg(t0.Add(time.Hour), "ZCZC VA40", "SEC GU", "NNNN")
	b := msg(t0.Add(2*time.Hour), "~{}ABC", "DEF")
	c := msg(t0.Add(3*time.Hour), "ZCZC DE34", "TEXT", "NNNN")
	d := msg(t0.Add(4*time.Hour), "ZCZC NA12", "NNNN")

	var bl builder
	// group 1: the long text, then two after its end; group 2: three that share a page
	pg1, _ := bl.put(txtLong, a, b)
	pg2, _ := bl.put(c, d)
	rows := []row{
		{t: t0, khz: 518, sq: 61, id: "B\x00", typ: "E\x00", num: "95\x00\x00\x00", area: 4, station: "ZBR Bermuda,BER    [LZ1AQ 1.2m dia.]", page: pg1, idx: 0},
		{t: t0.Add(time.Hour), khz: 490, sq: 37, id: "V\x00", typ: "A\x00", num: "40\x00\x00\x00", area: 4, station: "VAR-3 Chebogue (Fundy),CAN", page: pg1, idx: 1},
		{t: t0.Add(2 * time.Hour), khz: 518, sq: 3, id: "?\x00", typ: "?\x00", num: "???\x00\x00", station: "UNID", page: pg1, idx: 2},
		{t: t0.Add(3 * time.Hour), khz: 4209, sq: 20, id: "D\x00", typ: "E\x00", num: "5\x00\x00\x00\x00", station: "UNID", page: pg2, idx: 0},
		{t: t0.Add(4 * time.Hour), khz: 518, sq: 9, id: "N\x00", typ: "A\x00", num: "12\x00\x00\x00", station: "UNID", page: pg2, idx: 1},
		{t: t0.Add(5 * time.Hour), khz: 518, sq: 1, id: "?\x00", typ: "?\x00", num: "0\x00\x00\x00\x00", station: "UNID", page: 999, idx: 0}, // text gone
	}
	data := bl.file(rows)
	recs, st, err := ReadABS(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 6 || st.Rows != 6 || st.NoText != 1 {
		t.Fatalf("records %d, stats %+v", len(recs), st)
	}
	r := recs[0]
	if r.Channel != 518000 || r.B1 != "B" || r.B2 != "E" || r.Serial != "95" || r.NavArea != 4 || r.Quality != 61 || r.Station != "ZBR Bermuda,BER" ||
		r.Comment != "antenna: LZ1AQ 1.2m dia." || r.Kind != "message" || len(r.Lines) != len(long) || !r.Received.Equal(t0) {
		t.Fatalf("long record: %+v (%d lines)", r, len(r.Lines))
	}
	if r.Lines[0].Text != "ZCZC BE95" || r.Lines[1].Quality != 51 || r.Lines[len(r.Lines)-1].Text != "NNNN" {
		t.Fatalf("long record lines: %+v ... %+v", r.Lines[:2], r.Lines[len(r.Lines)-1])
	}
	if recs[1].Station != "VAR-3 Chebogue (Fundy),CAN" || recs[1].Channel != 490000 || len(recs[1].Lines) != 3 || recs[1].Lines[1].Text != "SEC GU" {
		t.Fatalf("record after the long text: %+v", recs[1])
	}
	if x := recs[2]; x.B1 != "" || x.B2 != "" || x.Serial != "" || x.Kind != "fragment" || len(x.Lines) != 2 {
		t.Fatalf("fragment: %+v", x)
	}
	if x := recs[3]; x.Channel != 4209500 || x.Serial != "05" || len(x.Lines) != 3 {
		t.Fatalf("4209 kHz record: %+v", x)
	}
	if x := recs[5]; len(x.Lines) != 0 || x.Station != "UNID" {
		t.Fatalf("record without text: %+v", x)
	}
	for i, x := range recs {
		if x.ID != int64(i+1) {
			t.Fatalf("numbering: %d is %d", i, x.ID)
		}
	}
}

func TestKind(t *testing.T) {
	if Kind([]byte("ABS0LUTEDATABASE\x00\x00")) != "abs" {
		t.Fatal("abs")
	}
	if Kind([]byte("YYYYMMDD HHMM KHz ID  CALL STATION,COUNTRY             DIST (Km)\n---\n20261002 0815 518 $04B ZBR Bermuda,BER   1286 [x]\n")) != "report" {
		t.Fatal("report")
	}
	if Kind([]byte(`{"op":"put","r":{}}`)) != "" || Kind([]byte("hello")) != "" {
		t.Fatal("other")
	}
}

const sampleReport = `YYYYMMDD HHMM KHz ID  CALL STATION,COUNTRY             DIST (Km)
----------------------------------------------------------------
20261002 0815 518 $04B ZBR Bermuda,BER                 1286 [LZ1AQ 1.2m dia.]
20261002 0852 518 $04F NMF Boston,USA                   414 [LZ1AQ 1.2m dia.]

RAW LOG
---
20261002 0815 518 $04B ZBR Bermuda,BER                 1286 [LZ1AQ 1.2m dia.]
2026-10-02 08:15:05 81> ZCZC BE95
2026-10-02 08:15:07 83> BERMUDA RADIO
2026-10-02 08:15:08 82> 261002 0215Z
2026-10-02 08:15:09 82> NNNN

---
20261002 0852 518 $04F NMF Boston,USA                   414 [LZ1AQ 1.2m dia.]
2026-10-02 08:52:42 102> ZCZC FA81
2026-10-02 08:52:47 100> SECNY BNM 0510-26 

---
20260118 0852 490 $00? UNID                               0 
2026-01-18 08:52:10> {}{}E ~~~X  IZ~H~~~{}
2026-01-18 09:10:36> XQM~ER~ ~QYW

---
20260119 0330 490 $04V VAR-3 Chebogue (Fundy),CAN       836 
2026-01-19 03:30:13> ZCZC VA40
2026-01-19 03:31:04> NNNN
`

func TestReadReport(t *testing.T) {
	recs, err := ReadReport(strings.NewReader(sampleReport))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("%d records", len(recs))
	}
	// oldest first
	u, v, a, b := recs[0], recs[1], recs[2], recs[3]
	if u.Station != "UNID" || u.B1 != "" || u.Kind != "fragment" || u.Channel != 490000 || u.Lines[0].Quality != 0 {
		t.Fatalf("unidentified: %+v", u)
	}
	if v.Station != "VAR-3 Chebogue (Fundy),CAN" || v.B1 != "V" || v.B2 != "A" || v.Serial != "40" || v.NavArea != 4 || v.DistanceKm != 836 || v.Kind != "message" {
		t.Fatalf("VAR-3: %+v", v)
	}
	if a.Station != "ZBR Bermuda,BER" || a.B2 != "E" || a.Serial != "95" || a.DistanceKm != 1286 || a.Comment != "antenna: LZ1AQ 1.2m dia." || a.Kind != "message" ||
		len(a.Lines) != 4 || a.Lines[0].Quality != 81 || !a.Received.Equal(time.Date(2026, 10, 2, 8, 15, 5, 0, time.UTC)) || !a.End.Equal(time.Date(2026, 10, 2, 8, 15, 9, 0, time.UTC)) {
		t.Fatalf("Bermuda: %+v", a)
	}
	if b.Station != "NMF Boston,USA" || b.Kind != "incomplete" || b.Quality != 101 {
		t.Fatalf("Boston: %+v", b)
	}
	if _, err := ReadReport(strings.NewReader("nothing here\n")); err == nil {
		t.Fatal("a file that is not a report was read")
	}
}
