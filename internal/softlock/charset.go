package softlock

// The CCIR 476 / SITOR-B character set (ITU-R M.476-5) in the form this package uses: a code is a byte holding the seven
// bits with the bit received FIRST in the highest of the seven positions (so the received string "1110001", the
// letter A, is 0b1110001). That is the "LSB first = first bit received" reading of the standard.
//
// Derived from navtex-core/src/charset.rs of navtex-rs (https://github.com/bgm60/navtex-rs, bgm60), GPL-3.0-or-later.

// Code is one 7-bit code in the low 7 bits of a byte.
type Code = uint8

// The two phasing signals sent before and between messages; never message content.
const (
	Phasing1 Code = 0b1111000
	Phasing2 Code = 0b0110011
)

type control int

const (
	ctlNone control = iota
	ctlCR
	ctlLF
	ctlLetters
	ctlFigures
	ctlSpace
	ctlBlank
)

// traffic holds the 26 codes that are a letter in letters mode and something else in figures mode. A figure of 0 means
// the standard leaves that position unassigned.
var traffic = [26]struct {
	code   Code
	letter rune
	figure rune
}{
	{0b1110001, 'A', '-'}, {0b0100111, 'B', '?'}, {0b1011100, 'C', ':'}, {0b1100101, 'D', 0},
	{0b0110101, 'E', '3'}, {0b1101100, 'F', 0}, {0b1010110, 'G', 0}, {0b1001011, 'H', 0},
	{0b1011001, 'I', '8'}, {0b1110100, 'J', '\a'}, {0b0111100, 'K', '('}, {0b1010011, 'L', ')'},
	{0b1001110, 'M', '.'}, {0b1001101, 'N', ','}, {0b1000111, 'O', '9'}, {0b1011010, 'P', '0'},
	{0b0111010, 'Q', '1'}, {0b1010101, 'R', '4'}, {0b1101001, 'S', '\''}, {0b0010111, 'T', '5'},
	{0b0111001, 'U', '7'}, {0b0011110, 'V', '='}, {0b1110010, 'W', '2'}, {0b0101110, 'X', '/'},
	{0b1101010, 'Y', '6'}, {0b1100011, 'Z', '+'},
}

var controls = [6]struct {
	code Code
	ctl  control
}{
	{0b0001111, ctlCR}, {0b0011011, ctlLF}, {0b0101101, ctlLetters}, {0b0110110, ctlFigures}, {0b0011101, ctlSpace}, {0b0101011, ctlBlank},
}

// trafficCodes lists the 32 codes that carry traffic (26 letters, 6 controls); the phasing signals are not included.
var trafficCodes = func() []Code {
	var out []Code
	for _, t := range traffic {
		out = append(out, t.code)
	}
	for _, c := range controls {
		out = append(out, c.code)
	}
	return out
}()

// weight is the number of 1-bits in a code; a valid code has weight 4.
func weight(c Code) int {
	n := 0
	for ; c != 0; c &= c - 1 {
		n++
	}
	return n
}

func isPhasing(c Code) bool { return c == Phasing1 || c == Phasing2 }

func controlOf(c Code) control {
	for _, k := range controls {
		if k.code == c {
			return k.ctl
		}
	}
	return ctlNone
}

// isValid reports whether c is one of the 34 codes the system uses (26 letters, 6 controls, 2 phasing signals).
func isValid(c Code) bool {
	if controlOf(c) != ctlNone || isPhasing(c) {
		return true
	}
	for _, t := range traffic {
		if t.code == c {
			return true
		}
	}
	return false
}

// printable gives the character of a letter code in the given shift; 0 if the code is no letter code or the figure is
// unassigned.
func printable(c Code, letters bool) rune {
	for _, t := range traffic {
		if t.code == c {
			if letters {
				return t.letter
			}
			return t.figure
		}
	}
	return 0
}

// hardCode turns seven soft values into a code: a value of zero or more is a 1; soft[0] is the first bit received.
func hardCode(soft *[7]float64) Code {
	var c Code
	for i, v := range soft {
		if v >= 0 {
			c |= 1 << (6 - i)
		}
	}
	return c
}
