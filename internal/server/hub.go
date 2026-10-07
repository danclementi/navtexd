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

	recent  []textChunk // newest text, for new clients
	recentN int
	lines   []lineMsg // newest completed lines
}

// LineKeep is how many completed lines a new client is given.
const LineKeep = 300

type lineMsg struct {
	T int64   `json:"t"` // unix milliseconds
	Q float64 `json:"q"`
	X string  `json:"x"`
}

// AddLine remembers a completed line for new clients.
func (h *Hub) AddLine(l lineMsg) {
	h.mu.Lock()
	h.lines = append(h.lines, l)
	if len(h.lines) > LineKeep {
		h.lines = h.lines[len(h.lines)-LineKeep:]
	}
	h.mu.Unlock()
}

// ClearLines forgets the lines of the message that was just completed.
func (h *Hub) ClearLines() {
	h.mu.Lock()
	h.lines = nil
	h.mu.Unlock()
}

// Lines returns the recent lines, oldest first.
func (h *Hub) Lines() []lineMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append(make([]lineMsg, 0, len(h.lines)), h.lines...)
}

// TextKeep is how many characters of recent text a new client is given.
const TextKeep = 6000

type textChunk struct {
	T int64  `json:"t"` // unix milliseconds
	X string `json:"x"`
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
func (h *Hub) Recent() []textChunk {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append(make([]textChunk, 0, len(h.recent)), h.recent...)
}

// AddText remembers text for new clients.
func (h *Hub) AddText(t time.Time, x string) {
	h.mu.Lock()
	h.recent = append(h.recent, textChunk{t.UnixMilli(), x})
	h.recentN += len(x)
	for h.recentN > TextKeep && len(h.recent) > 1 {
		h.recentN -= len(h.recent[0].X)
		h.recent = h.recent[1:]
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
