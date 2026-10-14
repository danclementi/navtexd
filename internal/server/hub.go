package server

import (
	"encoding/json"
	"sync"
	"time"
)

// event is one server-sent event.
type event struct {
	name string
	data []byte
}

type client struct {
	ch       chan event
	role     Role
	specGap  time.Duration // minimum time between spectrum frames for this client
	lastSpec time.Time
}

// Hub fans events out to the connected clients. A client that cannot keep up loses live meter frames; if it also misses
// a data event it is disconnected (it reconnects and gets the current state again).
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}

	recent  [2][]textChunk // newest text of each decoding channel, for new clients
	recentN [2]int
	lines   [2][]lineMsg // newest completed lines
}

// LineKeep is how many completed lines a new client is given.
const LineKeep = 300

type lineMsg struct {
	T   int64   `json:"t"` // unix milliseconds
	Q   float64 `json:"q"`
	X   string  `json:"x"`
	Rcv int     `json:"rcv,omitempty"` // decoding channel (0 = the first)
}

// AddLine remembers a completed line for new clients.
func (h *Hub) AddLine(l lineMsg) {
	h.mu.Lock()
	h.lines[l.Rcv] = append(h.lines[l.Rcv], l)
	if len(h.lines[l.Rcv]) > LineKeep {
		h.lines[l.Rcv] = h.lines[l.Rcv][len(h.lines[l.Rcv])-LineKeep:]
	}
	h.mu.Unlock()
}

// ClearLines forgets the lines of the message that was just completed.
func (h *Hub) ClearLines(rcv int) {
	h.mu.Lock()
	h.lines[rcv] = nil
	h.mu.Unlock()
}

// Lines returns the recent lines of one decoding channel, oldest first.
func (h *Hub) Lines(rcv int) []lineMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append(make([]lineMsg, 0, len(h.lines[rcv])), h.lines[rcv]...)
}

// TextKeep is how many characters of recent text a new client is given.
const TextKeep = 6000

type textChunk struct {
	T   int64  `json:"t"` // unix milliseconds
	X   string `json:"x"`
	Rcv int    `json:"rcv,omitempty"` // decoding channel (0 = the first)
}

// NewHub creates an empty hub.
func NewHub() *Hub { return &Hub{clients: map[*client]struct{}{}} }

// Clients returns the number of connected clients.
func (h *Hub) Clients() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.clients) }

func (h *Hub) add(role Role, specGap time.Duration) *client {
	c := &client{ch: make(chan event, 256), role: role, specGap: specGap}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) remove(c *client) { h.mu.Lock(); delete(h.clients, c); h.mu.Unlock() }

// Recent returns the recent text chunks, oldest first.
func (h *Hub) Recent(rcv int) []textChunk {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append(make([]textChunk, 0, len(h.recent[rcv])), h.recent[rcv]...)
}

// AddText remembers text for new clients.
func (h *Hub) AddText(rcv int, t time.Time, x string) {
	h.mu.Lock()
	h.recent[rcv] = append(h.recent[rcv], textChunk{t.UnixMilli(), x, rcv})
	h.recentN[rcv] += len(x)
	for h.recentN[rcv] > TextKeep && len(h.recent[rcv]) > 1 {
		h.recentN[rcv] -= len(h.recent[rcv][0].X)
		h.recent[rcv] = h.recent[rcv][1:]
	}
	h.mu.Unlock()
}

// Publish sends an event to every client. lossy events (meters, spectrum) are dropped for slow clients; a client that
// misses a non-lossy event is closed. adminOnly events go only to administrators.
func (h *Hub) Publish(name string, v any, lossy, adminOnly bool) {
	h.mu.Lock()
	n := len(h.clients)
	h.mu.Unlock()
	if n == 0 {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	ev := event{name, b}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if adminOnly && c.role != Admin {
			continue
		}
		if name == "spectrum" {
			if now.Sub(c.lastSpec) < c.specGap {
				continue
			}
			c.lastSpec = now
		}
		select {
		case c.ch <- ev:
		default:
			if !lossy {
				close(c.ch)
				delete(h.clients, c)
			}
		}
	}
}
