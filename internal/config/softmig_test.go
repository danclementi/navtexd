package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"navtex/internal/sitor"
)

// A settings file from before v0.9 (no marker): its channels were on hard, named or not, and move to soft once.
func TestSoftFramingMigratesOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	old := Default()
	old.Migrated = ""
	old.Decoder.Framing = sitor.FramingHard
	old.Extra[0].Enabled = true
	old.Extra[0].Decoder.Framing = sitor.FramingHard
	old.Extra[1].Decoder.Framing = sitor.FramingHard // (not enabled: moved, not reported)
	if err := old.Save(p); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	changed, ran := c.MigrateSoftFraming()
	if !ran || len(changed) != 2 || changed[0] != 1 || changed[1] != 2 {
		t.Fatalf("ran %v changed %v", ran, changed)
	}
	for ch := 0; ch < Channels; ch++ {
		if c.ForChannel(ch).Decoder.Framing != sitor.FramingSoft {
			t.Errorf("channel %d not soft", ch+1)
		}
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	// the operator puts channel 2 back on hard; the next start leaves it
	c, _ = Load(p)
	c.EditChannel(1, func(d *Config) { d.Decoder.Framing = sitor.FramingHard })
	_ = c.Save(p)
	c, _ = Load(p)
	if changed, ran := c.MigrateSoftFraming(); ran || changed != nil || c.ForChannel(1).Decoder.Framing != sitor.FramingHard {
		t.Errorf("migrated again: %v %v", ran, changed)
	}
}

// An old file that does not name the framing at all was on the old default, hard: it is reported too.
func TestSoftFramingOldFileWithoutKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"channel": {"frequency_hz": 518000}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Decoder.Framing != sitor.FramingHard {
		t.Fatal("an old file should read as hard")
	}
	if changed, ran := c.MigrateSoftFraming(); !ran || len(changed) != 1 || changed[0] != 1 {
		t.Errorf("changed %v ran %v", changed, ran)
	}
	if !strings.Contains(c.Migrated, MigratedSoft) {
		t.Error("no marker")
	}
}

func TestSoftIsTheDefault(t *testing.T) {
	if c := Default(); c.Decoder.Framing != sitor.FramingSoft || c.Extra[0].Decoder.Framing != sitor.FramingSoft {
		t.Error("soft framing is the default since v0.9")
	}
}
