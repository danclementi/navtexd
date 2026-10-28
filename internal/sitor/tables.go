// Package sitor decodes CCIR 476 / SITOR-B (forward error correction mode) from soft symbols.
package sitor

import (
	"fmt"
	"strings"
)

// Control tokens produced by the tables; they never reach the text output except LTRS/FIGS which switch shift.
const (
	tokLTRS = "<LTRS>"
	tokFIGS = "<FIGS>"
)

// LTRS and FIGS map the 35 valid 7-bit code values (bit 6..0 as printed, mark = 1) to characters.
// The code tables of CCIR 476 / ITU-R M.625.
var LTRS = map[uint8]string{
	0x0F: "<SIA>", 0x17: "J", 0x1B: "F", 0x1D: "C", 0x1E: "K", 0x27: "W", 0x2B: "Y", 0x2D: "P", 0x2E: "Q", 0x33: "<SIB>",
	0x35: "G", 0x36: "<FIGS>", 0x39: "M", 0x3A: "X", 0x3C: "V", 0x47: "A", 0x4B: "S", 0x4D: "I", 0x4E: "U", 0x53: "D",
	0x55: "R", 0x56: "E", 0x59: "N", 0x5A: "<LTRS>", 0x5C: " ", 0x63: "Z", 0x65: "L", 0x66: "<RPT>", 0x69: "H",
	0x6A: "<BLK>", 0x6C: "\n", 0x71: "O", 0x72: "B", 0x74: "T", 0x78: "\r",
}

var FIGS = map[uint8]string{
	0x0F: "<SIA>", 0x17: "<BEL>", 0x1B: "!", 0x1D: ":", 0x1E: "(", 0x27: "2", 0x2B: "6", 0x2D: "0", 0x2E: "1", 0x33: "<SIB>",
	0x35: "&", 0x36: "<FIGS>", 0x39: ".", 0x3A: "/", 0x3C: "=", 0x47: "-", 0x4B: "'", 0x4D: "8", 0x4E: "7", 0x53: "<ENQ>",
	0x55: "4", 0x56: "3", 0x59: ",", 0x5A: "<LTRS>", 0x5C: " ", 0x63: "+", 0x65: ")", 0x66: "<RPT>", 0x69: "£",
	0x6A: "<BLK>", 0x6C: "\n", 0x71: "9", 0x72: "?", 0x74: "5", 0x78: "\r",
}

var controls = map[string]bool{"<SIA>": true, "<SIB>": true, "<RPT>": true, "<BLK>": true, "<BEL>": true, "<ENQ>": true}

// Order says which bit of the code value is sent first.
type Order int

const (
	Auto Order = iota
	LSB        // LSB of the hex code first (assumed per M.476)
	MSB
)

func (o Order) String() string { return [...]string{"auto", "lsb", "msb"}[o] }

func (o Order) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

func (o *Order) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	case "auto":
		*o = Auto
	case "lsb":
		*o = LSB
	case "msb":
		*o = MSB
	default:
		return fmt.Errorf("unknown bit order %q (want auto, lsb or msb)", b)
	}
	return nil
}

// Framing selects how the character alignment is found.
type Framing int

const (
	FramingHard Framing = iota // hard 4-of-7 word framing (the original)
	FramingSoft                // soft repetition lock (internal/softlock)
)

func (f Framing) String() string { return [...]string{"hard", "soft"}[f] }

func (f Framing) MarshalText() ([]byte, error) { return []byte(f.String()), nil }

func (f *Framing) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	case "hard":
		*f = FramingHard
	case "soft":
		*f = FramingSoft
	default:
		return fmt.Errorf("unknown framing %q (want hard or soft)", b)
	}
	return nil
}

// Unreadable says what to print for a character whose DX and repeat copies were both received invalid.
type Unreadable int

const (
	Nearest Unreadable = iota // best guess: the closest valid character
	Star                      // "*"
	Tilde                     // "~" (as YaND prints it)
)

func (u Unreadable) String() string { return [...]string{"nearest", "star", "tilde"}[u] }

// Mark returns the character printed for an unreadable one under this policy ("" for Nearest).
func (u Unreadable) Mark() string { return [...]string{"", "*", "~"}[u] }

func (u Unreadable) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

func (u *Unreadable) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	case "nearest":
		*u = Nearest
	case "star", "*":
		*u = Star
	case "tilde", "~":
		*u = Tilde
	default:
		return fmt.Errorf("unknown unreadable-character policy %q (want nearest, star or tilde)", b)
	}
	return nil
}

// Polarity says which tone is the mark: Normal is the receiver in USB mode (mark is the higher audio tone), Inverted is
// LSB mode. Auto tries both.
type Polarity int

const (
	PolAuto Polarity = iota
	PolNormal
	PolInverted
)

func (p Polarity) String() string { return [...]string{"auto", "normal", "inverted"}[p] }

func (p Polarity) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

func (p *Polarity) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	case "auto":
		*p = PolAuto
	case "normal", "usb":
		*p = PolNormal
	case "inverted", "lsb":
		*p = PolInverted
	default:
		return fmt.Errorf("unknown polarity %q (want auto, normal or inverted)", b)
	}
	return nil
}

// allows reports whether a lock with the given mark direction (+1: mark is the higher tone) is permitted.
func (p Polarity) allows(pol int) bool {
	return p == PolAuto || (p == PolNormal && pol > 0) || (p == PolInverted && pol < 0)
}

// codeValue converts a pattern (bit t = t-th transmitted bit) into a code value for the given order.
func codeValue(mask uint8, o Order) uint8 {
	if o == LSB {
		return mask
	}
	var v uint8
	for t := 0; t < 7; t++ {
		if mask>>t&1 == 1 {
			v |= 1 << (6 - t)
		}
	}
	return v
}

// interp turns code values into text, tracking the letters/figures shift.
type interp struct {
	order Order
	fig   bool
	uos   bool // unshift on space: a space returns to the letters shift
}

// feed interprets one pattern. When unreadable is true and the policy marks them, a printable result becomes the mark
// (the shift state is still tracked from the best guess).
func (p *interp) feed(mask uint8, unreadable bool, policy Unreadable) string {
	v := codeValue(mask, p.order)
	tab := LTRS
	if p.fig {
		tab = FIGS
	}
	ch := tab[v]
	switch {
	case ch == tokFIGS:
		p.fig = true
	case ch == tokLTRS:
		p.fig = false
	case controls[ch]:
	default:
		if ch == " " && p.uos {
			p.fig = false
		}
		if unreadable && policy != Nearest {
			return policy.Mark()
		}
		return ch
	}
	return ""
}

// validPatterns lists the 35 seven-bit patterns with exactly four 1s, ascending.
var validPatterns = func() []uint8 {
	var out []uint8
	for m := 0; m < 128; m++ {
		n := 0
		for t := 0; t < 7; t++ {
			n += m >> t & 1
		}
		if n == 4 {
			out = append(out, uint8(m))
		}
	}
	return out
}()

// Reverse swaps the shift of each character: letters become the figures or symbols sent with the same code and the
// other way round. It repairs text that was decoded in the wrong shift (e.g. "-`54-(#-,J4-$89" for ASTRAKHAN RADIO).
// Characters without a counterpart (spaces, line ends, "~") are unchanged. The letter D (code ENQ in figures, which the
// decoder does not print) becomes "$" and "$" becomes D; J (BEL) is not reversed.
func Reverse(s string) string {
	toFig, toLet := map[rune]rune{}, map[rune]rune{}
	for code, l := range LTRS {
		f, ok := FIGS[code]
		lr, fr := []rune(l), []rune(f)
		if !ok || len(lr) != 1 || lr[0] < 'A' || lr[0] > 'Z' || lr[0] == 'J' {
			continue
		}
		if len(fr) == 1 {
			toFig[lr[0]], toLet[fr[0]] = fr[0], lr[0]
		}
	}
	toFig['D'], toLet['$'] = '$', 'D'
	out := []rune(s)
	for i, r := range out {
		if f, ok := toFig[r]; ok {
			out[i] = f
		} else if l, ok := toLet[r]; ok {
			out[i] = l
		}
	}
	return string(out)
}
