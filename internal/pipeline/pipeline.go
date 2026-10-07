// Package pipeline wires a sample Source through the DDC, FSK front end, SITOR decoder and message parser.
package pipeline

import (
	"context"
	"io"
	"time"

	"navtex/internal/config"
	"navtex/internal/engine"
	"navtex/internal/iq"
	"navtex/internal/navtex"
	"navtex/internal/sitor"
)

// EventKind identifies a pipeline event.
type EventKind int

const (
	EvMessage EventKind = iota // a complete NAVTEX message
	EvStatus                   // carrier / framing / bit-order changes (informational)
	EvText                     // raw decoded text (every character, before message parsing)
)

// Event is delivered to the sink. Time is seconds of stream time (sample index / sample rate) at the input.
type Event struct {
	Kind    EventKind
	Time    float64        // stream seconds
	Wall    time.Time      // Start + stream position for files, the system clock for live sources
	Message navtex.Message // EvMessage
	Text    string         // EvText, EvStatus
}

// Summary describes a finished run.
type Summary struct {
	Seconds       float64
	Samples       int64
	Messages      int
	SymbolsLocked int64
	BlankedFrac   float64
	FinalF0       float64
	CarrierFound  bool
	Order         sitor.Order
	DecimatedRate float64
	DecimatedBy   int
	ShiftHz       float64
}

// Run reads src until EOF (or ctx is cancelled) and calls sink for every event. sink runs on the calling goroutine.
// start is the UTC time of the first sample of a recording; the zero time means a live source, whose events are stamped
// with the system clock.
func Run(ctx context.Context, src iq.Source, cfg config.Config, start time.Time, sink func(Event)) (Summary, error) {
	var eng *engine.Engine
	var parser navtex.Parser
	var sum Summary
	wallAt := func(streamS float64) time.Time {
		if start.IsZero() {
			// live: the moment a stream position was received, less how far the input has advanced since then. This
			// follows the system clock, so sound-card clock drift never accumulates in the stamps.
			return time.Now().UTC().Add(-time.Duration((eng.Now() - streamS) * float64(time.Second)))
		}
		return start.UTC().Add(time.Duration(streamS * float64(time.Second)))
	}
	message := func(m navtex.Message) {
		if !m.Complete && !cfg.Output.IncompleteMessages {
			return
		}
		sum.Messages++
		sink(Event{Kind: EvMessage, Time: eng.Now(), Wall: wallAt(eng.Now()), Message: m})
	}
	eng, err := engine.New(src.SampleRate(), src.CentreHz(), src.Kind(), cfg, engine.Handlers{
		Text: func(text string, ts float64) {
			// a character is stamped with the moment it was sent (symbol position), not when it was decoded
			sink(Event{Kind: EvText, Time: ts, Wall: wallAt(ts), Text: text})
			for _, m := range parser.Feed(text) {
				message(m)
			}
		},
		Status: func(text string) {
			sink(Event{Kind: EvStatus, Time: eng.Now(), Wall: wallAt(eng.Now()), Text: text})
		},
	})
	if err != nil {
		return Summary{}, err
	}
	buf := make([]complex64, cfg.Input.BlockSize)
	for ctx.Err() == nil { // cancelled: finish like an end of stream so pending text and messages are flushed
		n, err := src.Read(buf)
		if n > 0 {
			eng.Process(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return sum, err
		}
	}
	eng.Flush()
	for _, m := range parser.Flush() {
		message(m)
	}
	es := eng.Summary()
	sum.Seconds, sum.Samples, sum.SymbolsLocked, sum.BlankedFrac = es.Seconds, es.Samples, es.SymbolsLocked, es.BlankedFrac
	sum.FinalF0, sum.CarrierFound, sum.Order = es.FinalF0, es.CarrierFound, es.Order
	sum.DecimatedRate, sum.DecimatedBy, sum.ShiftHz = es.DecimatedRate, es.DecimatedBy, es.ShiftHz
	return sum, ctx.Err()
}
