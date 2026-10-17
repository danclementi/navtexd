package app

import (
	"strings"

	"navtex/internal/segment"
	"navtex/internal/store"
)

// wordCount counts the words of a text: runs of letters (A-Z, any case) of at least minLetters letters. A run is
// ended by anything else, so an unreadable character (shown as ~ or *) splits a word. In autocorrected text a repaired
// word appears as [WATERS|WATER]; it counts once, as its first candidate.
func wordCount(text string, minLetters int) int { return wordCountV(text, minLetters, nil) }

// wordCountV is wordCount that, when known is not nil, counts only the runs it accepts (upper-case vocabulary words).
func wordCountV(text string, minLetters int, known func(string) bool) int {
	n, start := 0, -1
	end := 0
	flush := func() {
		if start >= 0 && end-start >= minLetters && (known == nil || known(strings.ToUpper(text[start:end]))) {
			n++
		}
		start = -1
	}
	skip := false // inside the later candidates of a [A|B|C] group
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '[':
			flush()
			skip = false
		case c == '|':
			flush()
			skip = true
		case c == ']':
			flush()
			skip = false
		case (c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') && !skip:
			if start < 0 {
				start = i
			}
			end = i + 1
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		default:
			flush()
		}
	}
	flush()
	return n
}

// fragmentWords is the number of words of a fragment record as the word test counts them: in the autocorrected text
// when the option asks for that and the record has one, else in the received text (with the operator's revisions).
func fragmentWords(rec store.Record, cfg segment.Config, known func(string) bool) int {
	text := strings.Join(rec.EffectiveLines(), "\n")
	if cfg.FragWordsCorrect && rec.Corrected != "" {
		text = rec.Corrected
	}
	return wordCountV(text, cfg.FragWordLetters, known)
}

// fragmentKept applies the word test to a record. Only fragments (text outside any ZCZC message) are tested. It returns
// whether to keep the record and, when not, how many words were found.
func fragmentKept(rec store.Record, cfg segment.Config, known func(string) bool) (bool, int) {
	if rec.Kind != "fragment" || cfg.MinFragmentWords <= 0 {
		return true, 0
	}
	n := fragmentWords(rec, cfg, known)
	return n >= cfg.MinFragmentWords, n
}

// useVocab tells whether the word test uses the vocabulary on a channel of the given frequency.
func useVocab(mode string, freqHz float64) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return freqHz >= 517000 && freqHz <= 519000
}
