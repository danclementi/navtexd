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

// IsUnitLine says whether a line can hold units: letters and the marks of unreadable characters only, at least one unit
// long, and not the NNNN of an international message.
func IsUnitLine(l string) bool {
	if len(l) < 3 || l == "NNNN" {
		return false
	}
	for i := 0; i < len(l); i++ {
		if c := l[i]; !isLetter(c) && c != '~' && c != '*' {
			return false
		}
	}
	return true
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
				best[j] = cell{t, i, text}
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
		out = append(out, piece{end, Unreadable})
	}
	for j := end; j > 0; j = best[j].from {
		out = append(out, piece{best[j].from, best[j].text})
	}
	for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
		out[a], out[b] = out[b], out[a]
	}
	return out
}

// Decode turns the lines of a message into text. A line of units that is wrap characters long goes on in the next line (the
// receiver breaks long lines), so such lines are cut together; each character is put in the line where its unit starts, so the result has
// as many lines as the input. Lines that are not units (ZCZC, the date-time group, NNNN) stay as they are. changed is false when no line holds units.
func Decode(lines []string, wrap int) (out []string, changed bool) {
	out = make([]string, len(lines))
	copy(out, lines)
	for i := 0; i < len(lines); {
		if !IsUnitLine(strings.TrimRight(lines[i], "\r")) {
			i++
			continue
		}
		j := i
		var sb strings.Builder
		var starts []int // line index of each letter offset
		for ; j < len(lines); j++ {
			l := strings.TrimRight(lines[j], "\r")
			if !IsUnitLine(l) {
				break
			}
			sb.WriteString(l)
			for range l {
				starts = append(starts, j)
			}
			if wrap <= 0 || len(l) < wrap {
				j++
				break
			}
		}
		parts := make([]strings.Builder, j-i)
		for _, p := range cut(sb.String()) {
			parts[starts[p.at]-i].WriteString(p.text)
		}
		for k := range parts {
			out[i+k] = parts[k].String()
		}
		changed = true
		i = j
	}
	return out, changed
}
