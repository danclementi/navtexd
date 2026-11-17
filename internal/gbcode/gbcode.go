// Package gbcode reads the Chinese text of the national NAVTEX service on 486 kHz.
//
// The stations send each GB 2312 character as three letters: the character's place in the 94 x 94 table (row * 94 + column,
// counted from 0) written as a number of three digits in base 26 with A = 0. The full-width digits, punctuation and Latin
// letters of GB 2312 go the same way, so 0 to 9 are AHV to AIE, ( and ) are AHN and AHO, and the end of a message is AIZ
// four times (the full-width NNNN). Characters that the receiver could not read show as ~ (or *); a letter that was lost
// or added moves every unit behind it, so the decoder looks for the way of cutting the line into units that gives
// the most likely text (common characters, few errors).
package gbcode

import "strings"

// Unreadable is what is shown for a unit that cannot be read.
const Unreadable = "□"

const base = 94 * 94

func isLetter(c byte) bool { return c >= 'A' && c <= 'Z' }

func isMark(c byte) bool { return c == '~' || c == '*' }

// IsUnitLine says whether a line can hold units: no blanks (a line of words is not Chinese text), at least one unit long, not
// the ZCZC line or the NNNN of an international message, and nearly all letters and marks of unreadable characters. A few
// other symbols or digits are allowed: where the receiver lost its shift a letter comes out as one, and "{}" marks a lost shift.
func IsUnitLine(l string) bool { return unitLine(strings.TrimRight(l, "\r")) }

func unitLine(l string) bool {
	if l == "NNNN" || strings.HasPrefix(l, "ZCZC") {
		return false
	}
	s := strings.ReplaceAll(l, "{}", "")
	if len(s) < 3 {
		return false
	}
	good := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case isLetter(c) || isMark(c):
			good++
		case c > ' ' && c < 0x7f: // a symbol or digit instead of a letter
		default:
			return false
		}
	}
	return good*10 >= len(s)*7
}

// clean makes the letters of a unit line: every symbol or digit is an unreadable letter, and "{}" (a lost shift) is dropped.
// gaps are the places in the result where a "{}" was.
func clean(l string) (s string, gaps []int) {
	var b strings.Builder
	for i := 0; i < len(l); {
		if strings.HasPrefix(l[i:], "{}") {
			gaps = append(gaps, b.Len())
			i += 2
			continue
		}
		c := l[i]
		i++
		if !isLetter(c) && !isMark(c) {
			c = '~'
		}
		b.WriteByte(c)
	}
	return b.String(), gaps
}

var rowRunes [94][]rune

func init() {
	for i, s := range rows {
		rowRunes[i] = []rune(s)
	}
}

func lookup(n int) (rune, bool) {
	if n < 0 || n >= base {
		return 0, false
	}
	r := rowRunes[n/94][n%94]
	return r, r != 0
}

// unit reads three letters. cost says how likely the result is a real character: 0 for the common ones (symbols, level 1
// hanzi), more for rare ones, 1 for a unit with an unreadable letter (shown as unreadable).
func unit(u string) (text string, cost float64) {
	n := 0
	for i := 0; i < 3; i++ {
		c := u[i]
		if c == '~' || c == '*' {
			return Unreadable, 1
		}
		if !isLetter(c) {
			return Unreadable, 9
		}
		n = n*26 + int(c-'A')
	}
	r, ok := lookup(n)
	if !ok {
		return Unreadable, 4
	}
	switch row := n/94 + 1; {
	case row == 1 || row == 3 || row >= 16 && row <= 55:
		return string(r), 0
	case row == 2 || row == 4:
		return string(r), 1.5
	case row >= 56 && row <= 87:
		return string(r), 2.5
	}
	return string(r), 4
}

type piece struct {
	at   int // where in the text the unit starts
	text string
	cost float64 // the unit's cost, 0 for a character that is common and was read without doubt
}

// cut cuts a stretch of letters into units. Besides a plain unit it allows one extra letter in front of a unit and a unit that is
// missing a letter (shown as unreadable), both at a price.
func cut(s string) []piece {
	n := len(s)
	if n == 0 {
		return nil
	}
	const inf = 1e18
	type cell struct {
		cost float64
		from int
		text string
		unit float64
	}
	best := make([]cell, n+1)
	for i := range best {
		best[i].cost = inf
	}
	best[0].cost = 0
	for i := 0; i < n; i++ {
		if best[i].cost >= inf {
			continue
		}
		try := func(step int, pen float64, text string, c float64) {
			j := i + step
			if j > n {
				return
			}
			if t := best[i].cost + c + pen; t < best[j].cost {
				best[j] = cell{t, i, text, c + pen}
			}
		}
		if i+3 <= n {
			t, c := unit(s[i : i+3])
			try(3, 0, t, c)
		}
		if i+4 <= n {
			t, c := unit(s[i+1 : i+4])
			try(4, 2, t, c)
		}
		if i+2 <= n {
			try(2, 2, Unreadable, 1)
		}
	}
	end := n
	for end > 0 && best[end].cost >= inf {
		end--
	}
	var out []piece
	if end < n { // a rest that is too short for a unit
		out = append(out, piece{end, Unreadable, 1})
	}
	for j := end; j > 0; j = best[j].from {
		out = append(out, piece{best[j].from, best[j].text, best[j].unit})
	}
	for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
		out[a], out[b] = out[b], out[a]
	}
	return out
}

// fadeBefore and fadeAfter are how many letters around a burst of errors cannot be trusted: in a fade the receiver prints
// letters that are noise, and the cut would find rare characters for them.
const (
	fadeBefore = 14
	fadeAfter  = 5
)

// untrusted marks the letters of a stream around a burst of two or more errors in a row and around a lost shift.
func untrusted(st string, gaps []int) []bool {
	bad := make([]bool, len(st)+1)
	mark := func(a, b int) {
		for k := max(a, 0); k < b && k < len(bad); k++ {
			bad[k] = true
		}
	}
	for i := 0; i < len(st); {
		if !isMark(st[i]) {
			i++
			continue
		}
		j := i
		for j < len(st) && isMark(st[j]) {
			j++
		}
		if j-i >= 2 {
			mark(i-fadeBefore, j+fadeAfter)
		}
		i = j
	}
	for _, g := range gaps {
		mark(g-fadeBefore, g+fadeAfter)
	}
	return bad
}

// runWidth is the length of the lines of a run of unit lines that the receiver cut at a fixed width: the length of at
// least 40 letters that two or more lines have, the longest such. 0 if there is none.
func runWidth(lines []string) int {
	count := map[int]int{}
	w := 0
	for _, l := range lines {
		n := len(strings.TrimRight(l, "\r"))
		if n >= 40 {
			if count[n]++; count[n] >= 2 && n > w {
				w = n
			}
		}
	}
	return w
}

// stream is the letters of a stretch of lines that are cut together.
type stream struct {
	st     string
	starts []int // the line of each letter
	gaps   []int
}

func (s *stream) cut() []piece {
	ps := cut(s.st)
	bad := untrusted(s.st, s.gaps)
	for i := range ps {
		// Noise seldom spells a full-width digit, letter or sign (they are 3% of the codes), but it spells a hanzi or another
		// sign nearly every time: in the untrusted stretch only those are doubtful.
		if ps[i].text != Unreadable && !fullWidth(ps[i].text) && (bad[ps[i].at] || bad[min(ps[i].at+2, len(bad)-1)]) {
			ps[i].text, ps[i].cost = Unreadable, 1
		}
	}
	return ps
}

// streams cuts a run of unit lines into the stretches that belong together: a line that is wrap characters long, or as long
// as the other lines of the run (the receiver cut it there), goes on in the next line.
func streams(lines []string, from, to, wrap int) []stream {
	w := runWidth(lines[from:to])
	var out []stream
	for a := from; a < to; {
		var s stream
		var sb strings.Builder
		b := a
		for ; b < to; b++ {
			l := strings.TrimRight(lines[b], "\r")
			c, g := clean(l)
			for _, p := range g {
				s.gaps = append(s.gaps, sb.Len()+p)
			}
			sb.WriteString(c)
			for range c {
				s.starts = append(s.starts, b)
			}
			if !(wrap > 0 && len(l) >= wrap || w > 0 && len(l) >= w) {
				b++
				break
			}
		}
		s.st = sb.String()
		out = append(out, s)
		a = b
	}
	return out
}

// Decode turns the lines of a message into text. A line of units that is wrap characters long (or as long as the other lines
// around it) goes on in the next line (the receiver breaks long lines), so such lines are cut together; each character is put in
// the line where its unit starts, so the result has as many lines as the input. Lines that are not units (ZCZC, the date-time
// group, NNNN) stay as they are. changed is false when no line holds units. Units near a burst of errors show as unreadable
// rather than as the rare characters that the noise happens to spell.
func Decode(lines []string, wrap int) (out []string, changed bool) {
	out = make([]string, len(lines))
	copy(out, lines)
	for i := 0; i < len(lines); {
		if !IsUnitLine(lines[i]) {
			i++
			continue
		}
		j := i
		for j < len(lines) && IsUnitLine(lines[j]) {
			j++
		}
		for _, s := range streams(lines, i, j, wrap) {
			first := s.starts[0]
			last := s.starts[len(s.starts)-1]
			parts := make([]strings.Builder, last-first+1)
			for _, p := range s.cut() {
				parts[s.starts[p.at]-first].WriteString(p.text)
			}
			for k := range parts {
				out[first+k] = parts[k].String()
			}
		}
		changed = true
		i = j
	}
	return out, changed
}

// Looks says whether the lines of a message are Chinese text of the national service: at least 30 letters in lines without
// blanks, 18 or more long (a message of words has blanks), and most of the units read as common characters. It is meant for
// a message that was not received on 486 kHz, so that it is read all the same.
func Looks(lines []string) bool {
	var units, good int
	for i := 0; i < len(lines); {
		if !IsUnitLine(lines[i]) {
			i++
			continue
		}
		j := i
		for j < len(lines) && IsUnitLine(lines[j]) {
			j++
		}
		for _, s := range streams(lines, i, j, 0) {
			if len(s.st) < 18 {
				continue
			}
			for _, p := range s.cut() {
				units++
				if p.text != Unreadable && p.cost == 0 {
					good++
				}
			}
		}
		i = j
	}
	return units >= 10 && good*10 >= units*5
}

// fullWidth says whether a unit's character is a full-width digit, letter or sign, or the ideographic space, comma or full stop.
func fullWidth(t string) bool {
	for _, r := range t {
		if !(r >= 0xff00 && r <= 0xffef || r >= 0x3000 && r <= 0x3002) {
			return false
		}
	}
	return t != ""
}

// HasHan says whether text holds Chinese characters (not only the full-width digits and signs).
func HasHan(text string) bool {
	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
