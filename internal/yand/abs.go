// Package yand reads the logs of YaND, the NAVTEX decoder this program is modelled on: its database (YAND_DB.ABS, an Absolute
// Database file of the Delphi world) and its reception reports (NDX*.TXT). The result is a list of store records.
//
// The .ABS file is read without the database engine. What is used of its layout (found by comparing files with what YaND shows):
//   - 4096-byte pages. Bytes 380..419 of each page hold a header ("ABSP", a checksum, the page type at 388). The data of a page
//     is its other bytes (4056 of them), the header being left out, so a text may continue over the gap.
//   - Pages of type 10 hold fixed-size records of 118 bytes. A NAVTEXLOG record is: 0xFC, date (days since 0001-01-01), time
//     (milliseconds), frequency (kHz), signal quality (float64), ID letter (2 bytes), message type (2), message number (5),
//     NAVAREA (1), station (76, with the antenna note of the log after it), and 11 bytes that tell where the text is.
//   - Pages of type 11 hold the texts ("blobs"), each with a header that repeats its length. The page of a record's text and
//     the number of the text in that page are in the last 11 bytes; a text that does not fit continues in the following pages.
package yand

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"navtex/internal/store"
)

const (
	pageSize  = 4096
	hdrAt     = 380 // where the page header sits in a page
	hdrLen    = 40
	dataLen   = pageSize - hdrLen // 4056 bytes of data in a page
	recLen    = 118
	typeRows  = 10
	typeBlobs = 11
)

// Magic is the start of an Absolute Database file.
const Magic = "ABS0LUTEDATABASE"

// Kind says what a file is: "abs" (a YaND database), "report" (a YaND reception report) or "" (neither).
func Kind(head []byte) string {
	if bytes.HasPrefix(head, []byte(Magic)) {
		return "abs"
	}
	for i, line := range strings.Split(string(head), "\n") {
		if i > 60 {
			break
		}
		if reportHead.MatchString(strings.TrimRight(line, "\r")) {
			return "report"
		}
	}
	return ""
}

// Stats says what a read found.
type Stats struct {
	Rows     int // records in the database
	NoText   int // records whose text could not be found
	Mismatch int // records whose text does not start at the time of the record (text taken from elsewhere in that page, or none)
}

type file struct {
	r     io.ReaderAt
	pages int
	blobs map[int][]blob // texts that start in a page, by page
}

type blob struct {
	page, idx int
	length    int
	chained   bool
	first     []byte // the part in the first page
	over      int    // chained: how many bytes of the next page the header takes
	end       int    // not chained: where it ends in its page
}

func (f *file) page(i int) ([]byte, error) {
	p := make([]byte, pageSize)
	if _, err := f.r.ReadAt(p, int64(i)*pageSize); err != nil && !(err == io.EOF) {
		return nil, err
	}
	return p, nil
}

func pageType(p []byte) int { return int(binary.LittleEndian.Uint16(p[hdrAt+8:])) }

// data returns the 4056 data bytes of a page.
func data(p []byte) []byte {
	d := make([]byte, 0, dataLen)
	d = append(d, p[:hdrAt]...)
	return append(d, p[hdrAt+hdrLen:]...)
}

func u32(b []byte, o int) int { return int(binary.LittleEndian.Uint32(b[o:])) }

// parse lists the texts that follow each other in the data s of a page from q on (below limit), each with a header that has its
// length twice. ext is the data of the next page: a header, or all of a text, may begin at the very end of the page and go on there.
// The first text of a page (q = 380) has a header 2 bytes longer than the others. A text that does not fit in the page goes on at
// the start of the following pages (which are full until its last); if it is the first of its page, its header also names the
// last page. A page that has the end of such a text before q numbers its own texts from where that group goes on: the log record
// says "text n of the page where the group began".
func parse(s, ext []byte, q, pg, idx, limit int) []blob {
	var out []blob
	v := s
	if len(ext) > 0 {
		v = append(append(make([]byte, 0, len(s)+len(ext)), s...), ext...)
	}
	for q+8 < len(s) && q < limit {
		k, n := 0, 0
		for _, kk := range []int{8, 6} {
			if q+kk+16 > len(v) {
				continue
			}
			l := u32(v, q+kk)
			if l > 0 && l < 1<<28 && u32(v, q+kk+4) == 0 && u32(v, q+kk+8) == l && u32(v, q+kk+12) == 0 {
				k, n = kk, l
				break
			}
		}
		if n == 0 {
			break
		}
		if q == hdrAt && len(out) == 0 {
			idx = int(binary.LittleEndian.Uint16(v[q+2:])) // the number of the first text of the page (usually 0)
		}
		b := blob{page: pg, idx: idx + len(out), length: n}
		at := q + k + 16
		if q == hdrAt && k == 6 && n > len(s)-at {
			at += 10 // the number of the last page, and 6 more bytes
		}
		switch {
		case at >= len(s): // the header ends in the next page, and so the text is all there
			b.chained, b.over = true, at-len(s)
			if b.over+n > len(ext) || len(ext) == 0 && n > 0 {
				b.first = nil
			}
		case at+n > len(s):
			b.chained = true
			b.first = s[at:]
		default:
			b.first = s[at : at+n]
			b.end = at + n
		}
		out = append(out, b)
		if b.chained {
			break
		}
		q = at + n
	}
	return out
}

// tail says where a long text ends: the page, how far in it, and the group (the page where the first of the texts began; the
// log records name the texts by that page) with the number the next text in it gets.
type tail struct{ group, at, next int }

// scan finds the texts of all pages. The first 380 bytes of a page are used only by a text that goes on from the page before, and
// by the texts that follow it there; the texts that start in the page itself begin at 380.
func (f *file) scan() error {
	tails := map[int]tail{}
	note := func(group int, b blob) {
		if !b.chained {
			if b.end == dataLen { // ends with the page: the next text begins at the start of the next page
				tails[b.page+1] = tail{group, 0, b.idx + 1}
			}
			return
		}
		used := b.over + b.length - len(b.first) // bytes the text takes in the pages after its own
		tails[b.page+1+used/dataLen] = tail{group, used % dataLen, b.idx + 1}
	}
	for pg := 0; pg < f.pages; pg++ {
		p, err := f.page(pg)
		if err != nil {
			return err
		}
		if string(p[hdrAt:hdrAt+4]) != "ABSP" || pageType(p) != typeBlobs {
			continue
		}
		s := data(p)
		var ext []byte
		if pg+1 < f.pages {
			if np, err := f.page(pg + 1); err == nil {
				ext = data(np)
			}
		}
		own := parse(s, ext, hdrAt, pg, 0, len(s)) // a page that is full of the end of a long text has text, not a header, here
		if t, ok := tails[pg]; ok && t.at < dataLen-24 {
			limit := len(s)
			if len(own) > 0 {
				limit = hdrAt
			}
			if bl := parse(s, ext, t.at, pg, t.next, limit); len(bl) > 0 {
				f.blobs[t.group] = append(f.blobs[t.group], bl...)
				note(t.group, bl[len(bl)-1])
			}
		}
		if len(own) > 0 {
			f.blobs[pg] = append(own, f.blobs[pg]...)
			note(pg, own[len(own)-1])
		}
	}
	return nil
}

func (f *file) blobsIn(pg int) []blob { return f.blobs[pg] }

// text returns the whole text of a blob.
func (f *file) text(b blob) ([]byte, error) {
	if !b.chained {
		return b.first, nil
	}
	out := append([]byte(nil), b.first...)
	skip := b.over
	for pg := b.page + 1; len(out) < b.length && pg < f.pages; pg++ {
		p, err := f.page(pg)
		if err != nil {
			return nil, err
		}
		d := data(p)
		if skip >= len(d) {
			skip -= len(d)
			continue
		}
		out = append(out, d[skip:]...)
		skip = 0
	}
	if len(out) > b.length {
		out = out[:b.length]
	}
	return out, nil
}

var lineRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)(?: (\d+))?> ?(.*)$`)

// parseLines splits a text into lines (each starts with its time, and in later versions of YaND the signal level).
func parseLines(txt string) []store.Line {
	var out []store.Line
	for _, ln := range strings.Split(txt, "\n") {
		ln = strings.TrimRight(ln, "\r")
		m := lineRe.FindStringSubmatch(ln)
		if m == nil {
			if ln != "" && len(out) > 0 { // a line without a time continues the one before
				out[len(out)-1].Text += ln
			}
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.UTC)
		if err != nil {
			continue
		}
		var q float64
		if m[2] != "" {
			fmt.Sscan(m[2], &q)
		}
		out = append(out, store.Line{Time: t, Quality: store.F2(q), Text: latin1(m[3])})
	}
	return out
}

// latin1 makes valid UTF-8 of text that is one byte per character.
func latin1(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			r := make([]rune, len(s))
			for j := 0; j < len(s); j++ {
				r[j] = rune(s[j])
			}
			return string(r)
		}
	}
	return s
}

// cstr is the text up to the first zero byte.
func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return latin1(string(b))
}

var noteRe = regexp.MustCompile(`^(.*?)\s{2,}\[(.*?)\]?\s*$`)

// ReadABS reads the records of a YaND database of the given size.
func ReadABS(r io.ReaderAt, size int64) ([]store.Record, Stats, error) {
	var st Stats
	head := make([]byte, 64)
	if _, err := r.ReadAt(head, 0); err != nil && err != io.EOF {
		return nil, st, err
	}
	if !bytes.HasPrefix(head, []byte(Magic)) {
		return nil, st, errors.New("not a YaND database (YAND_DB.ABS)")
	}
	if ps := int(binary.LittleEndian.Uint16(head[26:])); ps != pageSize {
		return nil, st, fmt.Errorf("unsupported page size %d in the database", ps)
	}
	f := &file{r: r, pages: int(size / pageSize), blobs: map[int][]blob{}}
	if err := f.scan(); err != nil {
		return nil, st, err
	}
	var rows [][]byte
	for pg := 0; pg < f.pages; pg++ {
		p, err := f.page(pg)
		if err != nil {
			return nil, st, err
		}
		if string(p[hdrAt:hdrAt+4]) != "ABSP" || pageType(p) != typeRows {
			continue
		}
		s := data(p)
		for o := 0; o+recLen <= len(s); o++ {
			if s[o] != 0xFC {
				continue
			}
			date, ms, fq := u32(s, o+1), u32(s, o+5), u32(s, o+9)
			sq := math.Float64frombits(binary.LittleEndian.Uint64(s[o+13:]))
			if date < 725000 || date > 800000 || ms >= 86400000 || fq < 100 || fq > 30000 || !(sq >= 0 && sq < 1000) {
				continue
			}
			rows = append(rows, append([]byte(nil), s[o:o+recLen]...))
			o += recLen - 1
		}
	}
	if len(rows) == 0 {
		return nil, st, errors.New("no log records found in the database (is it a YaND database of a version this program knows?)")
	}
	st.Rows = len(rows)
	recs := make([]store.Record, 0, len(rows))
	for _, b := range rows {
		date, ms, fq := u32(b, 1), u32(b, 5), u32(b, 9)
		sq := math.Float64frombits(binary.LittleEndian.Uint64(b[13:]))
		rx := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, date-1).Add(time.Duration(ms) * time.Millisecond)
		rec := store.Record{
			Version: 1, Received: rx, End: rx, Quality: store.F2(sq), Channel: fq * 1000,
			B1: letter(b[21:23]), B2: letter(b[23:25]), NavArea: int(b[30]),
		}
		if fq == 4209 {
			rec.Channel = 4209500
		}
		num := cstr(b[25:30])
		station := cstr(b[31 : 31+76])
		if m := noteRe.FindStringSubmatch(station); m != nil {
			station = strings.TrimSpace(m[1])
			rec.Comment = "antenna: " + strings.TrimSpace(m[2])
		}
		if station == "" {
			station = "UNID"
		}
		rec.Station, rec.IdentMethod = station, "YaND import"
		// the text: its page and the number of the text in the page
		pg, idx := u32(b, 107), int(binary.LittleEndian.Uint16(b[111:]))
		if idx == 0xFFFF {
			idx = 0
		}
		var lines []store.Line
		found := false
		if bl := f.blobsIn(pg); len(bl) > 0 {
			var cand []blob
			for _, x := range bl {
				if x.idx == idx {
					cand = append(cand, x)
				}
			}
			for _, x := range bl {
				if x.idx != idx {
					cand = append(cand, x)
				}
			}
			for ci, c := range cand {
				txt, err := f.text(c)
				if err != nil {
					return nil, st, err
				}
				ls := parseLines(string(txt))
				if len(ls) > 0 && ls[0].Time.Equal(rx.Truncate(time.Second)) {
					lines, found = ls, true
					break
				}
				if ci == 0 {
					st.Mismatch++
				}
			}
		}
		if !found {
			st.NoText++
		}
		rec.Lines = lines
		if len(lines) > 0 {
			rec.End = lines[len(lines)-1].Time
		}
		if rec.B1 != "" && num != "" {
			rec.Serial = serial(num)
		}
		rec.Kind = kindOf(&rec)
		recs = append(recs, rec)
	}
	finish(recs)
	return recs, st, nil
}

// letter is the ID letter in a 2-byte field; "?" (unknown) is empty.
func letter(b []byte) string {
	if c := b[0]; c >= 'A' && c <= 'Z' {
		return string(c)
	}
	return ""
}

var digits = regexp.MustCompile(`^\d{1,2}$`)

func serial(s string) string {
	if !digits.MatchString(s) {
		return ""
	}
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// kindOf says whether a record is a whole message (start and end seen), an incomplete one, or a fragment.
func kindOf(r *store.Record) string {
	start := r.B1 != ""
	end := false
	for i, l := range r.Lines {
		t := strings.TrimSpace(l.Text)
		if i == 0 && strings.HasPrefix(t, "ZCZC") {
			start = true
		}
		if t == "NNNN" {
			end = true
		}
	}
	switch {
	case start && end:
		return "message"
	case start:
		return "incomplete"
	}
	return "fragment"
}

// finish puts records in time order and numbers them.
func finish(recs []store.Record) {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Received.Before(recs[j].Received) })
	for i := range recs {
		recs[i].ID = int64(i + 1)
		if recs[i].Lines == nil {
			recs[i].Lines = []store.Line{}
		}
	}
}
