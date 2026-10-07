package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"navtex/internal/sitor"
)

func TestDefaultsValidateAndRoundTrip(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "navtex.json")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip differs:\n%s\nvs\n%s", got.JSON(), c.JSON())
	}
}

func TestLoadPartialAndUnknown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	os.WriteFile(p, []byte(`{"channel":{"frequency_hz":490000},"decoder":{"unreadable":"star","bit_order":"msb"}}`), 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Channel.FrequencyHz != 490000 || c.Decoder.Unreadable != sitor.Star || c.Decoder.Order != sitor.MSB {
		t.Fatalf("%+v", c.Decoder)
	}
	if c.Receiver.BlankK != Default().Receiver.BlankK || c.Decoder.MinRun != 10 {
		t.Error("unset keys must keep their defaults")
	}
	os.WriteFile(p, []byte(`{"decoder":{"unreadble":"star"}}`), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("unknown key accepted")
	}
	os.WriteFile(p, []byte(`{"decoder":{"unreadable":"question"}}`), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("bad enum accepted")
	}
}

func TestValidate(t *testing.T) {
	c := Default()
	c.Output.Format = "xml"
	c.Input.StartUTC = "yesterday"
	c.Receiver.TrackWindow = 10
	err := c.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"output.format", "start_utc", "tracking"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

// Every option must be documented so a GUI can label it.
func TestFieldsDocumented(t *testing.T) {
	fs := Fields(Default())
	if len(fs) < 40 {
		t.Fatalf("only %d fields found", len(fs))
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if f.Doc == "" {
			t.Errorf("%s has no doc tag", f.Path)
		}
		if seen[f.Path] {
			t.Errorf("duplicate path %s", f.Path)
		}
		seen[f.Path] = true
		if f.Kind == "enum" && len(f.Choices) == 0 {
			t.Errorf("%s: enum without choices tag", f.Path)
		}
	}
	for _, p := range []string{"decoder.unreadable", "decoder.break_after_brk", "receiver.blanker_threshold", "receiver.demod.baud", "channel.frequency_hz"} {
		if !seen[p] {
			t.Errorf("expected option %s", p)
		}
	}
}

func TestSet(t *testing.T) {
	c := Default()
	for path, val := range map[string]string{"receiver.blanker_threshold": "0", "decoder.unreadable": "star",
		"output.verbose": "true", "input.file": "C:\\rec\\x.wav", "receiver.demod.timing_gain": "0.25"} {
		if err := Set(&c, path, val); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if c.Receiver.BlankK != 0 || c.Decoder.Unreadable != sitor.Star || !c.Output.Verbose ||
		c.Input.File != `C:\rec\x.wav` || c.Receiver.Alpha != 0.25 {
		t.Fatalf("%s", c.JSON())
	}
	before := c.JSON()
	for _, bad := range [][2]string{{"decoder.nonsense", "1"}, {"decoder.unreadable", "maybe"}, {"output.format", "xml"}} {
		if err := Set(&c, bad[0], bad[1]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if string(before) != string(c.JSON()) {
		t.Error("failed Set changed the configuration")
	}
}

// Every option is either live (IsLive) or needs a restart (RestartNeeded), and the two agree.
func TestLiveListMatchesRestartRule(t *testing.T) {
	base := Default()
	for _, f := range Fields(base) {
		if strings.HasPrefix(f.Path, "server.") {
			continue // navtexd restart, not the channel
		}
		next := base
		var v string
		switch f.Kind {
		case "bool":
			v = fmt.Sprint(!f.Value.(bool))
		case "int":
			v = fmt.Sprint(reflect.ValueOf(f.Value).Int() + 1)
		case "float":
			v = fmt.Sprint(reflect.ValueOf(f.Value).Float() + 1)
		case "enum":
			for _, ch := range f.Choices {
				if ch != f.Value {
					v = ch
					break
				}
			}
		default:
			v = "x"
		}
		switch f.Path {
		case "input.source":
			v = "stdin"
		case "input.pcm_format":
			v = "f32le"
		case "input.start_utc":
			v = "2026-01-01 00:00:00"
		case "meters.spectrum_fft":
			v = "512"
		case "meters.spectrum_smooth", "meters.quality_eye_weight":
			v = "0.5"
		case "output.format":
			v = "json"
		}
		if err := Set(&next, f.Path, v); err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		if got, want := RestartNeeded(base, next), !IsLive(f.Path); got != want {
			t.Errorf("%s: RestartNeeded=%v but IsLive=%v", f.Path, got, !want)
		}
	}
	// the blanker: a new threshold is live, switching it on or off is not
	a, b, c := base, base, base
	a.Receiver.BlankK, b.Receiver.BlankK, c.Receiver.BlankK = 6, 9, 0
	if RestartNeeded(a, b) || !RestartNeeded(a, c) {
		t.Error("blanker rule wrong")
	}
}

// The viewer position option moved from server.viewer_position_hints to operator.public_location_info; an old
// settings file must still load and keep its meaning.
func TestViewerHintsMovedToOperator(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"server":{"viewer_position_hints":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Operator.Public || c.Server.ViewerHints {
		t.Errorf("not carried over: public=%v old=%v", c.Operator.Public, c.Server.ViewerHints)
	}
	for _, f := range Fields(c) {
		if f.Path == "server.viewer_position_hints" {
			t.Error("the obsolete option is still listed")
		}
	}
}
