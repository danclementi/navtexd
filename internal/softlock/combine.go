package softlock

// combiner decides one character from its two copies by adding their soft values bit by bit, and keeps the
// letters/figures shift state between characters.
type combiner struct {
	letters bool
}

func newCombiner() combiner { return combiner{letters: true} }

// combine returns the character to print for the pair (dx, rx: seven soft values each, first bit first, positive = 1).
// ok is false when the pair prints nothing (a pair of phasing signals, a shift, idle filler). '~' means an error; '!'
// means the two copies each looked like a valid but different character.
func (c *combiner) combine(dxSoft, rxSoft *[7]float64) (ch rune, ok bool) {
	dx, rx := hardCode(dxSoft), hardCode(rxSoft)
	if isPhasing(dx) && isPhasing(rx) {
		return 0, false
	}
	var sum [7]float64
	for i := range sum {
		sum[i] = dxSoft[i] + rxSoft[i]
	}
	code := hardCode(&sum)
	switch weight(code) {
	case 4:
		return c.decode(code)
	case 3, 5:
		if fixed, found := correctSingleBit(code, &sum); found {
			return c.decode(fixed)
		}
	}
	dxValid, rxValid := weight(dx) == 4, weight(rx) == 4
	var chosen Code
	switch {
	case dxValid && rxValid && dx == rx:
		chosen = dx
	case dxValid && rxValid:
		return '!', true
	case dxValid:
		chosen = dx
	case rxValid:
		chosen = rx
	default:
		return '~', true
	}
	return c.decode(chosen)
}

func (c *combiner) decode(code Code) (rune, bool) {
	switch controlOf(code) {
	case ctlLetters:
		c.letters = true
		return 0, false
	case ctlFigures:
		c.letters = false
		return 0, false
	case ctlCR:
		return '\r', true
	case ctlLF:
		return '\n', true
	case ctlSpace:
		return ' ', true
	case ctlBlank:
		return 0, false
	}
	if r := printable(code, c.letters); r != 0 {
		return r, true
	}
	return '~', true
}

// correctSingleBit repairs a code of weight 3 or 5 by flipping the least certain bit that makes it a valid code (the
// earlier one if two are equally uncertain).
func correctSingleBit(code Code, combined *[7]float64) (Code, bool) {
	var flipOnes bool
	switch weight(code) {
	case 3:
		flipOnes = false
	case 5:
		flipOnes = true
	default:
		return 0, false
	}
	found := false
	var bestConf float64
	var best Code
	for i, v := range combined {
		mask := Code(1) << (6 - i)
		if (code&mask != 0) != flipOnes {
			continue
		}
		flipped := code ^ mask
		if !isValid(flipped) {
			continue
		}
		conf := v
		if conf < 0 {
			conf = -conf
		}
		if !found || conf < bestConf {
			found, bestConf, best = true, conf, flipped
		}
	}
	return best, found
}
