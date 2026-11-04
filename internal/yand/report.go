package yand

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"navtex/internal/store"
)

// A reception report of YaND (NDX*.TXT) lists the messages of a day or period; its RAW LOG part holds each message: a line
// "20261002 0815 518 $04B ZBR Bermuda,BER   1286 [antenna]" and then the lines of the text, each with its time.
var reportHead = regexp.MustCompile(`^(\d{8}) (\d{4}) +(\d+) +\$(\d\d)(.)(?: +(.*?))?\s*$`)

var (
	noteTail = regexp.MustCompile(`\s*\[(.*)\]\s*$`)
	distTail = regexp.MustCompile(`\s+(\d+)\s*$`)
	zczc     = regexp.MustCompile(`^ZCZC +([A-Z])([A-Z])(\d\d)\s*$`)
)

// ReadReport reads the messages of a YaND reception report.
func ReadReport(r io.Reader) ([]store.Record, error) {
	var recs []store.Record
	var cur *store.Record
	var hdrTime time.Time
	flush := func() {
		if cur == nil {
			return
		}
		if len(cur.Lines) > 0 { // the list at the top of a report has headers only
			cur.Received = cur.Lines[0].Time
			if cur.Received.Truncate(time.Minute) != hdrTime {
				cur.Received = hdrTime
			}
			cur.End = cur.Lines[len(cur.Lines)-1].Time
			var sum float64
			for _, l := range cur.Lines {
				sum += float64(l.Quality)
			}
			cur.Quality = store.F2(sum / float64(len(cur.Lines)))
			if cur.B1 != "" {
				if m := zczc.FindStringSubmatch(strings.TrimSpace(cur.Lines[0].Text)); m != nil && m[1] == cur.B1 {
					cur.B2, cur.Serial = m[2], m[3]
				}
			}
			cur.Kind = kindOf(cur)
			recs = append(recs, *cur)
		}
		cur = nil
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		ln := strings.TrimRight(sc.Text(), "\r")
		if m := reportHead.FindStringSubmatch(ln); m != nil {
			flush()
			t, err := time.ParseInLocation("200601021504", m[1]+m[2], time.UTC)
			if err != nil {
				continue
			}
			hdrTime = t
			khz, _ := strconv.Atoi(m[3])
			rec := store.Record{Version: 1, Channel: khz * 1000, IdentMethod: "YaND import"}
			if khz == 4209 {
				rec.Channel = 4209500
			}
			rec.NavArea, _ = strconv.Atoi(m[4])
			if m[5] != "?" {
				rec.B1 = m[5]
			}
			rest := m[6]
			if n := noteTail.FindStringSubmatch(rest); n != nil {
				rec.Comment = "antenna: " + strings.TrimSpace(n[1])
				rest = rest[:len(rest)-len(n[0])]
			}
			if d := distTail.FindStringSubmatch(rest); d != nil {
				rec.DistanceKm, _ = strconv.ParseFloat(d[1], 64)
				rest = rest[:len(rest)-len(d[0])]
			} else if strings.TrimSpace(rest) == "0" {
				rest = ""
			}
			rec.Station = strings.TrimSpace(rest)
			if rec.Station == "" {
				rec.Station = "UNID"
			}
			cur = &rec
			continue
		}
		if cur == nil {
			continue
		}
		if ls := parseLines(ln); len(ls) == 1 && lineRe.MatchString(ln) {
			cur.Lines = append(cur.Lines, ls[0])
		} else if strings.TrimSpace(ln) == "---" {
			flush()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	if len(recs) == 0 {
		return nil, errors.New("no messages found in the report (is it a YaND reception report?)")
	}
	finish(recs)
	return recs, nil
}
