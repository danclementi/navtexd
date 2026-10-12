package app

import (
	"strings"

	"navtex/internal/segment"
	"navtex/internal/store"
)

// wordCount counts the words of a text: runs of letters (A-Z, any case) of at least minLetters letters. A run is
// ended by anything else, so an unreadable character (shown as ~ or *) splits a word. In autocorrected text a repaired
// word appears as [WATERS|WATER]; it counts once, as its first candidate.
func wordCount(text string, minLetters int) int {
	n, run := 0, 0
	flush := func() {
		if run >= minLetters {
			n++
		}
		run = 0
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
			run++
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
func fragmentWords(rec store.Record, cfg segment.Config) int {
	text := strings.Join(rec.EffectiveLines(), "\n")
	if cfg.FragWordsCorrect && rec.Corrected != "" {
		text = rec.Corrected
	}
	return wordCount(text, cfg.FragWordLetters)
}

// fragmentKept applies the word test to a record. Only fragments (text outside any ZCZC message) are tested. It returns
// whether to keep the record and, when not, how many words were found.
func fragmentKept(rec store.Record, cfg segment.Config) (bool, int) {
	if rec.Kind != "fragment" || cfg.MinFragmentWords <= 0 {
		return true, 0
	}
	n := fragmentWords(rec, cfg)
	return n >= cfg.MinFragmentWords, n
}
