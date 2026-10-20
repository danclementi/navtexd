package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"navtex/internal/config"
)

// The settings of each channel before it was edited are kept (a few snapshots, saved next to the settings) so that
// "restore previous settings" can undo a bout of changes. Edits made within histGap of the last snapshot belong to the
// same bout and do not take another one.
const (
	HistoryFile = "settings-history.json"
	histGap     = 2 * time.Minute
	histMax     = 10
)

type histEntry struct {
	At time.Time    `json:"at"`
	X  config.Extra `json:"settings"`
}

func (r *Runner) histPath() string { return filepath.Join(r.dir, HistoryFile) }

func (r *Runner) histLoad() {
	if r.hist != nil {
		return
	}
	r.hist = map[int][]histEntry{}
	if b, err := os.ReadFile(r.histPath()); err == nil {
		_ = json.Unmarshal(b, &r.hist)
	}
}

func (r *Runner) histSave() {
	if b, err := json.MarshalIndent(r.hist, "", " "); err == nil {
		_ = os.WriteFile(r.histPath(), b, 0o644)
	}
}

func extraOf(c config.Config, ch int) config.Extra {
	d := c.ForChannel(ch)
	return config.Extra{Input: d.Input, Channel: d.Channel, DDC: d.DDC, Receiver: d.Receiver, Decoder: d.Decoder, Segment: d.Segment}
}

// PushHistory remembers the settings of channel ch as they were in before. Unless force is set, a snapshot taken less
// than a couple of minutes ago stands for the whole bout of edits. Nothing is kept when the channel did not change.
func (r *Runner) PushHistory(ch int, before config.Config, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.histLoad()
	if extraOf(before, ch) == extraOf(r.cfg, ch) {
		return
	}
	h := r.hist[ch]
	if !force && len(h) > 0 && time.Since(h[len(h)-1].At) < histGap {
		return
	}
	h = append(h, histEntry{At: time.Now().UTC(), X: extraOf(before, ch)})
	if len(h) > histMax {
		h = h[len(h)-histMax:]
	}
	r.hist[ch] = h
	r.histSave()
}

// HistoryCount is how many earlier settings of channel ch can be restored.
func (r *Runner) HistoryCount(ch int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.histLoad()
	return len(r.hist[ch])
}

// RestorePrevious puts the settings of channel ch back to the latest snapshot (the name, comment and on/off switch
// stay) and forgets it, so that repeating goes further back.
func (r *Runner) RestorePrevious(ch int) (restart bool, err error) {
	r.mu.Lock()
	r.histLoad()
	h := r.hist[ch]
	r.mu.Unlock()
	if len(h) == 0 {
		return false, errors.New("there are no earlier settings of this channel to restore")
	}
	e := h[len(h)-1]
	restart, err = r.Modify(func(c *config.Config) error {
		c.EditChannel(ch, func(d *config.Config) {
			name, comment := d.Channel.Name, d.Channel.Comment
			d.Input, d.Channel, d.DDC, d.Receiver, d.Decoder, d.Segment = e.X.Input, e.X.Channel, e.X.DDC, e.X.Receiver, e.X.Decoder, e.X.Segment
			d.Channel.Name, d.Channel.Comment = name, comment
		})
		return nil
	})
	if err == nil {
		r.mu.Lock()
		r.hist[ch] = r.hist[ch][:len(r.hist[ch])-1]
		r.histSave()
		r.mu.Unlock()
	}
	return restart, err
}
