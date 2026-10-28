package sitor

import "strings"

type textItem struct {
	text string
	sym  int64
}

// shaper post-processes the character stream to keep the output tidy: a line break is inserted before ZCZC when it
// would not start a line, and after the station's BRK marker. Up to three characters (Z, ZC, ZCZ) are held back
// until it is clear whether they begin ZCZC.
type shaper struct {
	cfg     *Config
	events  *[]Event
	held    []textItem
	lineLen int
	recent  []rune // last four emitted characters
}

func (s *shaper) reset() {
	s.held, s.lineLen, s.recent = nil, 0, nil
}

func (s *shaper) put(t string, sym int64) {
	if s.cfg.BreakBeforeZCZC {
		if len(s.held) > 0 {
			cand := s.heldText() + t
			if strings.HasPrefix("ZCZC", cand) {
				s.held = append(s.held, textItem{t, sym})
				if cand == "ZCZC" {
					if s.lineLen > 0 {
						s.raw("\n", s.held[0].sym)
					}
					s.flush()
				}
				return
			}
			s.flush()
		}
		if t == "Z" {
			s.held = append(s.held, textItem{t, sym})
			return
		}
	}
	s.emit(t, sym)
}

func (s *shaper) heldText() string {
	var b strings.Builder
	for _, h := range s.held {
		b.WriteString(h.text)
	}
	return b.String()
}

// flush releases held characters unchanged.
func (s *shaper) flush() {
	h := s.held
	s.held = nil
	for _, it := range h {
		s.emit(it.text, it.sym)
	}
}

// raw sends text without touching the line state (used for inserted breaks).
func (s *shaper) raw(t string, sym int64) {
	*s.events = append(*s.events, Event{Kind: EvText, Text: t, Sym: sym})
	if strings.Contains(t, "\n") {
		s.lineLen = 0
	}
}

func (s *shaper) emit(t string, sym int64) {
	*s.events = append(*s.events, Event{Kind: EvText, Text: t, Sym: sym})
	for _, r := range t {
		switch r {
		case '\n':
			s.lineLen = 0
		case '\r':
		default:
			s.lineLen++
		}
		s.recent = append(s.recent, r)
		if len(s.recent) > 4 {
			s.recent = s.recent[1:]
		}
	}
	if s.cfg.BreakAfterBRK && len(s.recent) >= 3 {
		n := len(s.recent)
		if string(s.recent[n-3:]) == "BRK" && (n == 3 || !isLetter(s.recent[n-4])) {
			s.raw("\n", sym)
			s.recent = s.recent[:0]
		}
	}
}

func isLetter(r rune) bool { return r >= 'A' && r <= 'Z' }
