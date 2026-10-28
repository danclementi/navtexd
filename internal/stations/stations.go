// Package stations reads and writes the NAVTEX station table in the format used by YaND (YAND_STATIONS.txt), which the
// community keeps updated. The file is ASCII with CRLF line ends: a "VERSION5" line, then one station per line with 17
// or 18 fields separated by '#':
//
//	navarea#freq_kHz#B1#callsign#name#country_code#country#lat#lon#t1#t2#t3#t4#t5#t6#GID keywords#UID keywords#
//
// t1..t6 are the six daily transmission start times (UTC, hh:mm, four hours apart). GID keywords are geographical
// names and UID keywords unique names that appear in that station's messages; both are comma separated and may contain
// significant leading / trailing spaces (word-bounded matches such as " KARA "). A callsign of "___" means unknown.
package stations

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Station is one line of the table.
type Station struct {
	NavArea     int
	FreqKHz     int
	ID          string // B1 station identification letter
	Callsign    string
	Name        string
	CountryCode string
	Country     string
	Lat, Lon    float64
	Times       [6]string // transmission start times "hh:mm" UTC
	GID         []string  // geographical keywords
	UID         []string  // unique keywords
}

// Table is a parsed station file.
type Table struct {
	Version  string // first line, e.g. "VERSION5"
	Stations []Station
}

const fieldCount = 18 // including the empty field after the final '#'

// Parse reads a station table. Missing trailing fields are tolerated (one line of the shipped file has 17).
func Parse(r io.Reader) (*Table, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	t := &Table{}
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimRight(sc.Text(), "\r")
		if line == 1 {
			t.Version = text
			if !strings.HasPrefix(text, "VERSION") {
				return nil, fmt.Errorf("line 1: expected a VERSION header, got %q", text)
			}
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		f := strings.Split(text, "#")
		if len(f) < 15 {
			return nil, fmt.Errorf("line %d: %d fields, want at least 15", line, len(f))
		}
		for len(f) < fieldCount {
			f = append(f, "")
		}
		s := Station{ID: f[2], Callsign: f[3], Name: f[4], CountryCode: f[5], Country: f[6]}
		var err error
		if s.NavArea, err = strconv.Atoi(f[0]); err != nil {
			return nil, fmt.Errorf("line %d: navarea %q", line, f[0])
		}
		if s.FreqKHz, err = strconv.Atoi(f[1]); err != nil || (s.FreqKHz != 490 && s.FreqKHz != 518 && s.FreqKHz != 486 && s.FreqKHz != 424) {
			return nil, fmt.Errorf("line %d: frequency %q (want 490, 518, 486 or 424)", line, f[1])
		}
		if s.Lat, err = strconv.ParseFloat(f[7], 64); err != nil || s.Lat < -90 || s.Lat > 90 {
			return nil, fmt.Errorf("line %d: latitude %q", line, f[7])
		}
		if s.Lon, err = strconv.ParseFloat(f[8], 64); err != nil || s.Lon < -180 || s.Lon > 180 {
			return nil, fmt.Errorf("line %d: longitude %q", line, f[8])
		}
		for i := 0; i < 6; i++ {
			s.Times[i] = f[9+i]
			if _, ok := ParseClock(s.Times[i]); !ok {
				return nil, fmt.Errorf("line %d: time %q", line, s.Times[i])
			}
		}
		s.GID = splitKeywords(f[15])
		s.UID = splitKeywords(f[16])
		t.Stations = append(t.Stations, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if line == 0 {
		return nil, fmt.Errorf("empty station file")
	}
	return t, nil
}

// Write writes the table in the same format (CRLF line ends, 18 fields per line).
func (t *Table) Write(w io.Writer) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "%s\r\n", t.Version)
	for _, s := range t.Stations {
		fmt.Fprintf(bw, "%d#%d#%s#%s#%s#%s#%s#%s#%s#%s#%s#%s#\r\n", s.NavArea, s.FreqKHz, s.ID, s.Callsign, s.Name,
			s.CountryCode, s.Country, strconv.FormatFloat(s.Lat, 'f', 3, 64), strconv.FormatFloat(s.Lon, 'f', 3, 64),
			strings.Join(s.Times[:], "#"), strings.Join(s.GID, ","), strings.Join(s.UID, ","))
	}
	return bw.Flush()
}

func splitKeywords(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// ParseClock converts "hh:mm" to minutes after midnight.
func ParseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, e1 := strconv.Atoi(s[:2])
	m, e2 := strconv.Atoi(s[3:])
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
