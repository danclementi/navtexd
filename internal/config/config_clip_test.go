package config

import "testing"

func TestReview_ClipMemoryLimit(t *testing.T) {
	c := Default()
	if c.Clips.MaxMemoryMB != 500 {
		t.Fatalf("default %d", c.Clips.MaxMemoryMB)
	}
	c.Clips.MaxMemoryMB = 0
	if err := c.Validate(); err == nil {
		t.Fatal("0 MB accepted")
	}
}

func TestSharedManualCarrierFollowsNominalPosition(t *testing.T) {
	prev := Default()
	prev.Channel.FrequencyHz = 518000
	prev.Input.Source = "hpsdr"
	prev.Input.HPSDR.LoBelowHz = 3000
	prev.Extra[0].Enabled = true
	prev.EditChannel(1, func(c *Config) { c.Channel.FrequencyHz = 490000; c.Input.Source = "hpsdr" })
	manual := func(c Config) float64 { return c.ForChannel(1).ManualCarrierHz() }

	// the channel is set to share: its carrier is at 490000 - (518000 - 3000) = -25000
	next := prev
	next.EditChannel(1, func(c *Config) { c.Input.SharedFrom = 1 })
	SyncSharedCarriers(prev, &next)
	if got := manual(next); got != -25000 {
		t.Fatalf("manual carrier %v after sharing, want -25000", got)
	}
	// the marker is dragged; an unrelated change leaves it alone
	prev = next
	next.EditChannel(1, func(c *Config) { t := c.Tuning(); t.ManualCarrierHz = -24900; c.SetTuning(t) })
	next.Operator.Comment = "x"
	SyncSharedCarriers(prev, &next)
	if got := manual(next); got != -24900 {
		t.Fatalf("manual carrier %v, a dragged value must stay", got)
	}
	// the owner's lo_below_hz changes: the nominal position moves
	prev = next
	next.Input.HPSDR.LoBelowHz = 4000
	SyncSharedCarriers(prev, &next)
	if got := manual(next); got != -24000 {
		t.Fatalf("manual carrier %v after the owner's LO moved, want -24000", got)
	}
	// the sharer's own channel frequency changes
	prev = next
	next.EditChannel(1, func(c *Config) { c.Channel.FrequencyHz = 518000 })
	SyncSharedCarriers(prev, &next)
	if got := manual(next); got != 4000 {
		t.Fatalf("manual carrier %v after the channel changed, want 4000", got)
	}
	// the owner's value is not touched
	if next.ManualCarrierHz() != prev.ManualCarrierHz() {
		t.Error("the owner's manual carrier changed")
	}
}
