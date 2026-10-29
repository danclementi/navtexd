// Package compare finds the differences between what two decoding channels made of the same message, without
// depending on small differences in timing: messages are paired by their identification (ZCZC group) and overlapping
// time, and their texts are aligned character by character.
package compare

import (
	"fmt"
	"strings"
)

// Op is one difference between text A and text B.
type Op struct {
	Kind byte // 'd' the channels read different characters, 'u' unreadable on one side only, 'a' only A has it, 'b' only B has it
	Pos  int  // position in the normalised text A (rune index) where the difference starts
	A, B string
}

// Normalize makes the texts of two channels comparable: upper case, every run of white space a single space, and every
// unreadable mark (~ or *) the same mark.
func Normalize(s string) []rune {
	var out []rune
	space := true
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !space {
				out = append(out, ' ')
			}
			space = true
			continue
		case r == '*':
			r = '~'
		}
		out = append(out, r)
		space = false
	}
	if n := len(out); n > 0 && out[n-1] == ' ' {
		out = out[:n-1]
	}
	return out
}

const band = 300 // the alignment gives up when the texts differ by more than this many characters

// Align returns the differences between a and b (normalised). ok is false when the texts are too different to align
// (then the count is a length-based lower bound, ops is empty).
func Align(a, b []rune) (ops []Op, ok bool) {
	n, m := len(a), len(b)
	if d := n - m; d > band || -d > band {
		return nil, false
	}
	const inf = 1 << 30
	w := 2*band + 1
	// cost[i][j-i+band]
	cost := make([]uint16, (n+1)*w)
	get := func(i, j int) int {
		k := j - i + band
		if k < 0 || k >= w || j < 0 || j > m {
			return inf
		}
		return int(cost[i*w+k])
	}
	for i := 0; i <= n; i++ {
		for k := 0; k < w; k++ {
			j := i + k - band
			if j < 0 || j > m {
				cost[i*w+k] = 0xFFFF
				continue
			}
			var c int
			switch {
			case i == 0:
				c = j
			case j == 0:
				c = i
			default:
				sub := 1
				if a[i-1] == b[j-1] {
					sub = 0
				}
				c = get(i-1, j-1) + sub
				if v := get(i-1, j) + 1; v < c {
					c = v
				}
				if v := get(i, j-1) + 1; v < c {
					c = v
				}
			}
			if c > 0xFFFE {
				c = 0xFFFE
			}
			cost[i*w+k] = uint16(c)
		}
	}
	if get(n, m) >= band {
		return nil, false
	}
	// trace back
	type step struct {
		kind   byte // '=', 's', 'a' (A only), 'b' (B only)
		ai, bj int
	}
	var steps []step
	i, j := n, m
	for i > 0 || j > 0 {
		cur := get(i, j)
		switch {
		case i > 0 && j > 0 && a[i-1] == b[j-1] && get(i-1, j-1) == cur:
			steps = append(steps, step{'=', i - 1, j - 1})
			i, j = i-1, j-1
		case i > 0 && j > 0 && get(i-1, j-1)+1 == cur:
			steps = append(steps, step{'s', i - 1, j - 1})
			i, j = i-1, j-1
		case i > 0 && get(i-1, j)+1 == cur:
			steps = append(steps, step{'a', i - 1, j})
			i--
		default:
			steps = append(steps, step{'b', i, j - 1})
			j--
		}
	}
	for s := len(steps) - 1; s >= 0; s-- {
		st := steps[s]
		switch st.kind {
		case 's':
			x, y := a[st.ai], b[st.bj]
			k := byte('d')
			if x == '~' || y == '~' {
				k = 'u'
			}
			ops = append(ops, Op{k, st.ai, string(x), string(y)})
		case 'a':
			ops = append(ops, Op{'a', st.ai, string(a[st.ai]), ""})
		case 'b':
			ops = append(ops, Op{'b', st.ai, "", string(b[st.bj])})
		}
	}
	return ops, true
}

// Describe summarises the kinds of difference, e.g. "3 differences: 1 different character, 1 only in ch3, 1 unreadable
// in one".
func Describe(ops []Op, nameA, nameB string) string {
	var d, u, a, b int
	for _, o := range ops {
		switch o.Kind {
		case 'd':
			d++
		case 'u':
			u++
		case 'a':
			a++
		case 'b':
			b++
		}
	}
	var parts []string
	add := func(n int, s string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	add(d, "different")
	add(u, "unreadable in one")
	add(a, "only in "+nameA)
	add(b, "only in "+nameB)
	pl := "s"
	if len(ops) == 1 {
		pl = ""
	}
	return fmt.Sprintf("%d difference%s (%s)", len(ops), pl, strings.Join(parts, ", "))
}

// Excerpts shows each difference with some context, one per line: "pos 123: ...LOCAL TIME 14|A|... / ...|B|...".
func Excerpts(a, b []rune, ops []Op, nameA, nameB string, limit int) string {
	var sb strings.Builder
	for i, o := range ops {
		if i == limit {
			fmt.Fprintf(&sb, "... and %d more\n", len(ops)-i)
			break
		}
		lo := max0(o.Pos - 20)
		hi := min(len(a), o.Pos+21)
		fmt.Fprintf(&sb, "at %d: %s: %q   %s: %q   in context %q\n", o.Pos, nameA, o.A, nameB, o.B, string(a[lo:hi]))
	}
	return sb.String()
}

func max0(x int) int {
	if x < 0 {
		return 0
	}
	return x
}
