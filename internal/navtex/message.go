// Package navtex extracts NAVTEX messages (ZCZC B1B2B3B4 ... NNNN) from a stream of decoded text.
package navtex

import "strings"

// Message is one NAVTEX message. Every ZCZC starts a new message. A message normally ends with NNNN (Complete);
// one cut short by the next ZCZC or by the end of the stream is still reported, with Complete false.
type Message struct {
	Station  string // B1: transmitter identification
	Subject  string // B2: subject indicator
	Serial   string // B3B4: serial number 01..99 (00 = urgent)
	Body     string
	Complete bool // NNNN was received
}

// ID is B1B2B3B4, e.g. "FA97". Characters that could not be read may appear as '*' (or a wrong letter).
func (m Message) ID() string { return m.Station + m.Subject + m.Serial }

// Parser extracts messages from a stream of text chunks.
type Parser struct {
	buf    string
	active bool // buf starts with the ZCZC of a message in progress
}

const maxBuf = 64 << 10

// SplitHeader splits the text that follows ZCZC into the B1 station letter, the B2 subject letter, the B3B4 serial
// number and the rest of the text. Short or damaged headers give empty fields.
func SplitHeader(raw string) (station, subject, serial, body string) {
	raw = strings.TrimLeft(raw, " \t\r\n")
	id := raw
	if i := strings.IndexAny(raw, " \t\r\n"); i >= 0 && i <= 4 {
		id, body = raw[:i], raw[i:]
	} else if len(raw) > 4 {
		id, body = raw[:4], raw[4:]
	} else {
		id = raw
	}
	pad := id + "    "
	return strings.TrimSpace(pad[0:1]), strings.TrimSpace(pad[1:2]), strings.TrimSpace(pad[2:4]), body
}

func build(raw string, complete bool) Message {
	station, subject, serial, body := SplitHeader(raw)
	body = strings.TrimSpace(body)
	if !complete {
		// the station sends BRK before a new ZCZC; it is not part of the interrupted message
		body = strings.TrimSpace(strings.TrimSuffix(body, "BRK"))
	}
	return Message{Station: station, Subject: subject, Serial: serial, Body: body, Complete: complete}
}

// Feed appends text and returns any messages that it finished.
func (p *Parser) Feed(text string) []Message {
	p.buf += text
	var out []Message
	for {
		if !p.active {
			i := strings.Index(p.buf, "ZCZC")
			if i < 0 {
				if len(p.buf) > 3 {
					p.buf = p.buf[len(p.buf)-3:]
				}
				return out
			}
			p.buf, p.active = p.buf[i:], true
		}
		rest := p.buf[4:]
		n, z := strings.Index(rest, "NNNN"), strings.Index(rest, "ZCZC")
		switch {
		case n >= 0 && (z < 0 || n < z):
			out = append(out, build(rest[:n], true))
			p.buf, p.active = rest[n+4:], false
		case z >= 0:
			out = append(out, build(rest[:z], false))
			p.buf = p.buf[4+z:]
		case len(p.buf) > maxBuf:
			out = append(out, build(rest, false))
			p.buf, p.active = "", false
			return out
		default:
			return out
		}
	}
}

// Flush ends the stream: a message still in progress is reported as incomplete.
func (p *Parser) Flush() []Message {
	var out []Message
	if p.active && len(strings.TrimSpace(p.buf[4:])) > 0 {
		out = append(out, build(p.buf[4:], false))
	}
	p.buf, p.active = "", false
	return out
}
