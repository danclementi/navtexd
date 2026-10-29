// Package segment cuts the decoded character stream into records: complete messages (ZCZC..NNNN), messages that were
// cut short, and fragments of text received outside any message. Each record keeps its lines with their send time and
// signal quality.
package segment

import (
	"strings"

	"navtex/internal/navtex"
)

// Kind classifies a record.
type Kind int

const (
	Message    Kind = iota // ZCZC ... NNNN
	Incomplete             // ZCZC ... but no NNNN before the next ZCZC or a long silence
	Fragment               // text outside any message
)

func (k Kind) String() string { return [...]string{"message", "incomplete", "fragment"}[k] }

// Line is one line of a record. StreamS is the send time of its first character in stream seconds.
type Line struct {
	StreamS float64
	Quality float64 // 0..100, mean over the line
	Text    string
}

// Record is one received transmission.
type Record struct {
	Kind         Kind
	StartS, EndS float64 // stream seconds of the first and last character
	Lines        []Line
	B1, B2       string // station and subject letters ("" for fragments or damaged headers)
	Serial       string
	StartFix     string  // the damaged start as received ("ZCZ~", "CZC") when it was recognized by its position and structure; "" = a clean ZCZC
	Quality      float64 // mean over the record
}

// Body returns the text of the message without the ZCZC header line and the closing NNNN.
func (r Record) Body() string {
	var b strings.Builder
	for i, l := range r.Lines {
		t := l.Text
		if i == 0 && r.Kind != Fragment && (strings.HasPrefix(t, "ZCZC") || r.StartFix != "") {
			continue
		}
		if r.Kind == Message && i == len(r.Lines)-1 && strings.TrimSpace(t) == "NNNN" {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// Text returns all lines joined with newlines.
func (r Record) Text() string {
	ts := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		ts[i] = l.Text
	}
	return strings.Join(ts, "\n")
}

// Config controls the segmenter.
type Config struct {
	MinFragmentChars int     `json:"min_fragment_chars" unit:"characters" doc:"Do not keep fragments (text outside any ZCZC message) with fewer visible characters than this (0 = keep all)"`
	MinFragmentWords int     `json:"min_fragment_words" doc:"Keep a fragment only if it contains at least this many words of min_fragment_word_letters letters or more (0 = no word test). Many fragments are noise without a single recognizable word"`
	FragWordLetters  int     `json:"min_fragment_word_letters" unit:"letters" doc:"Shortest run of letters that counts as a word in the fragment word test"`
	FragVocab        string  `json:"fragment_words_vocabulary" choices:"auto,on,off" doc:"Count only words found in the vocabulary (on), any run of letters (off), or decide by channel (auto: on at 518 kHz, off at 490 and 424 kHz, where the vocabulary does not fit the language). The vocabulary holds words of 5 letters or more, so with this on shorter words do not count"`
	FragWordsCorrect bool    `json:"fragment_words_after_autocorrect" doc:"Count the words after autocorrection, so that words repaired from the vocabulary count; off = count the received text as it is"`
	IdleCloseS       float64 `json:"idle_close_s" unit:"s" doc:"A message or fragment with no new character for this long is closed"`
	MaxLineChars     int     `json:"max_line_chars" unit:"characters" doc:"Force a line break after this many characters without one"`
}

func DefaultConfig() Config {
	return Config{MinFragmentChars: 16, FragWordLetters: 4, FragVocab: "auto", FragWordsCorrect: true, IdleCloseS: 60, MaxLineChars: 200}
}

// Segmenter builds records from text chunks. quality(a, b) returns the signal quality between two stream times.
type Segmenter struct {
	cfg     Config
	quality func(a, b float64) float64
	emit    func(Record)

	// OnLine, if set, is called for every line as it is completed (before its record is finished).
	OnLine func(Line)

	cur   *Record
	buf   []rune
	bufT  []float64 // send time of each rune of buf
	lastS float64
}

// SetConfig changes the options of a running segmenter; they apply to the text that follows.
func (s *Segmenter) SetConfig(cfg Config) {
	if cfg.MaxLineChars < 20 {
		cfg.MaxLineChars = 200
	}
	s.cfg = cfg
}

// New creates a segmenter.
func New(cfg Config, quality func(a, b float64) float64, emit func(Record)) *Segmenter {
	if cfg.MaxLineChars < 20 {
		cfg.MaxLineChars = 200
	}
	if quality == nil {
		quality = func(a, b float64) float64 { return 0 }
	}
	return &Segmenter{cfg: cfg, quality: quality, emit: emit}
}

// Feed adds decoded text sent at stream time t.
func (s *Segmenter) Feed(text string, t float64) {
	for _, r := range text {
		switch {
		case r == '\r':
			continue
		case r == '\n':
			if s.cur == nil {
				continue
			}
			s.endLine(t)
			continue
		case s.cur == nil:
			if r == ' ' {
				continue
			}
			s.cur = &Record{Kind: Fragment, StartS: t}
		}
		s.buf = append(s.buf, r)
		s.bufT = append(s.bufT, t)
		s.lastS = t
		n := len(s.buf)
		switch {
		case n >= 4 && string(s.buf[n-4:]) == "ZCZC":
			s.startMessage(t)
		case n >= 4 && (string(s.buf[n-4:]) == "NNNN" || s.cur.Kind == Message && damagedEnd(s.buf[n-4:])):
			nT := s.bufT[n-4]
			s.buf, s.bufT = s.buf[:n-4], s.bufT[:n-4]
			if len(s.buf) > 0 {
				s.endLine(nT)
			}
			s.cur.Lines = append(s.cur.Lines, Line{StreamS: nT, Quality: s.quality(nT, t), Text: "NNNN"})
			s.close(t, true)
		case n >= s.cfg.MaxLineChars:
			s.endLine(t)
		}
	}
}

// damagedEnd reports whether four characters are NNNN with exactly one of them unreadable ("NN~N"). It is only used
// inside a message, where a lone damaged character in the closing group is far likelier than anything else.
func damagedEnd(t []rune) bool {
	marks := 0
	for _, r := range t {
		switch r {
		case 'N':
		case '~', '*':
			marks++
		default:
			return false
		}
	}
	return marks == 1
}

// Tick closes a record that has been silent for too long; t is the current stream time.
func (s *Segmenter) Tick(t float64) {
	if s.cur != nil && t-s.lastS > s.cfg.IdleCloseS {
		s.endLine(s.lastS)
		s.close(s.lastS, false)
	}
}

// Flush closes whatever is in progress.
func (s *Segmenter) Flush(t float64) {
	if s.cur != nil {
		s.endLine(s.lastS)
		s.close(s.lastS, false)
	}
}

func (s *Segmenter) startMessage(t float64) {
	n := len(s.buf)
	zStart := s.bufT[n-4]
	// what precedes ZCZC on the line belongs to the previous record
	s.buf, s.bufT = s.buf[:n-4], s.bufT[:n-4]
	if s.cur != nil {
		s.endLine(s.lastS)
		s.close(s.lastS, false)
	}
	s.cur = &Record{Kind: Message, StartS: zStart}
	s.buf = append(s.buf[:0], 'Z', 'C', 'Z', 'C')
	s.bufT = append(s.bufT[:0], zStart, zStart, t, t)
}

// damagedStart finds a message start whose ZCZC was not decoded correctly: the line ends with the identification group
// (two letters, two figures, either may contain unreadable marks) preceded by a space and by something that is ZCZC
// with up to two characters unreadable or one character missing ("ZCZ~", "CZC", "~Z~C"). It returns where that token
// begins and its length.
func damagedStart(rs []rune) (idx, n int, ok bool) {
	for len(rs) > 0 && rs[len(rs)-1] == ' ' {
		rs = rs[:len(rs)-1]
	}
	if len(rs) < 8 {
		return 0, 0, false
	}
	mark := func(r rune) bool { return r == '~' || r == '*' }
	id := rs[len(rs)-4:]
	for i, r := range id {
		if i < 2 && !(r >= 'A' && r <= 'Z' || mark(r)) || i >= 2 && !(r >= '0' && r <= '9' || mark(r)) {
			return 0, 0, false
		}
	}
	j := len(rs) - 5
	if rs[j] != ' ' {
		return 0, 0, false
	}
	const z = "ZCZC"
	fit := func(t []rune, want string) bool {
		m := 0
		for i, r := range t {
			switch {
			case r == rune(want[i]):
				m++
			case mark(r):
			default:
				return false
			}
		}
		return m >= 2
	}
	if j >= 4 {
		if t := rs[j-4 : j]; string(t) != z && fit(t, z) {
			return j - 4, 4, true
		}
	}
	if j >= 3 {
		t := rs[j-3 : j]
		for k := 0; k < 4; k++ {
			if fit(t, z[:k]+z[k+1:]) {
				return j - 3, 3, true
			}
		}
	}
	return 0, 0, false
}

func (s *Segmenter) endLine(t float64) {
	if s.cur != nil && s.cur.Kind == Fragment {
		if idx, n, ok := damagedStart(s.buf); ok {
			tok := string(s.buf[idx : idx+n])
			rest, restT := append([]rune(nil), s.buf[idx:]...), append([]float64(nil), s.bufT[idx:]...)
			s.buf, s.bufT = s.buf[:idx], s.bufT[:idx]
			if strings.TrimSpace(string(s.buf)) != "" {
				s.addLine(t)
			}
			s.close(restT[0], false)
			s.cur = &Record{Kind: Message, StartS: restT[0], StartFix: tok}
			s.buf, s.bufT = rest, restT
		}
	}
	s.addLine(t)
}

func (s *Segmenter) addLine(t float64) {
	if s.cur == nil {
		s.buf, s.bufT = s.buf[:0], s.bufT[:0]
		return
	}
	text := string(s.buf)
	if text == "" && len(s.cur.Lines) == 0 {
		return
	}
	start := t
	if len(s.bufT) > 0 {
		start = s.bufT[0]
	} else if n := len(s.cur.Lines); n > 0 {
		start = s.cur.Lines[n-1].StreamS
	}
	ln := Line{StreamS: start, Quality: s.quality(start, t), Text: text}
	s.cur.Lines = append(s.cur.Lines, ln)
	s.buf, s.bufT = s.buf[:0], s.bufT[:0]
	if s.OnLine != nil && strings.TrimSpace(text) != "" {
		s.OnLine(ln)
	}
}

func (s *Segmenter) close(t float64, nnnn bool) {
	r := s.cur
	s.cur = nil
	if r == nil {
		return
	}
	for len(r.Lines) > 0 && strings.TrimSpace(r.Lines[len(r.Lines)-1].Text) == "" {
		r.Lines = r.Lines[:len(r.Lines)-1]
	}
	if len(r.Lines) == 0 {
		return
	}
	r.EndS = t
	r.Quality = s.quality(r.StartS, t)
	switch {
	case r.Kind == Message && !nnnn:
		r.Kind = Incomplete
	case r.Kind == Fragment:
		visible := 0
		for _, l := range r.Lines {
			for _, c := range l.Text {
				if c != ' ' && c != '\t' {
					visible++
				}
			}
		}
		if visible < s.cfg.MinFragmentChars {
			return
		}
	}
	if r.Kind != Fragment {
		head := strings.TrimPrefix(r.Lines[0].Text, "ZCZC")
		if r.StartFix != "" {
			head = strings.TrimPrefix(r.Lines[0].Text, r.StartFix)
		}
		r.B1, r.B2, r.Serial, _ = navtex.SplitHeader(head)
	}
	s.emit(*r)
}
