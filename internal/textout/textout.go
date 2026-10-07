// Package textout writes decoded text as time-stamped lines.
package textout

import (
	"io"
	"strings"
	"time"
)

// TimeFormat is the prefix layout: "2026-01-26 10:38:59 > ".
const TimeFormat = "2006-01-02 15:04:05"

// LineWriter puts (times are converted to UTC)  "yyyy-mm-dd hh:mm:ss > " in front of every new line, using the time of the line's first character.
// Carriage returns are dropped; a line feed ends the line.
type LineWriter struct {
	W       io.Writer
	inLine  bool
	scratch strings.Builder
}

// Write outputs text that was received at time t.
func (l *LineWriter) Write(text string, t time.Time) {
	l.scratch.Reset()
	for _, r := range text {
		if r == '\r' {
			continue
		}
		if !l.inLine {
			l.scratch.WriteString(t.UTC().Format(TimeFormat))
			l.scratch.WriteString(" > ")
			l.inLine = true
		}
		l.scratch.WriteRune(r)
		if r == '\n' {
			l.inLine = false
		}
	}
	if l.scratch.Len() > 0 {
		io.WriteString(l.W, l.scratch.String())
	}
}

// Finish ends a partial last line.
func (l *LineWriter) Finish() {
	if l.inLine {
		io.WriteString(l.W, "\n")
		l.inLine = false
	}
}
