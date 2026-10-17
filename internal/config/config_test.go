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
	c.Input.File.StartUTC = "yesterday"
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
		"output.verbose": "true", "input.file.path": "C:\\rec\\x.wav", "receiver.demod.timing_gain": "0.25"} {
		if err := Set(&c, path, val); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if c.Receiver.BlankK != 0 || c.Decoder.Unreadable != sitor.Star || !c.Output.Verbose ||
		c.Input.File.Path != `C:\rec\x.wav` || c.Receiver.Alpha != 0.25 {
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

// extraPath splits "extra.1.decoder.min_run" into (1, "decoder.min_run"); other paths come back unchanged with ok false.
func extraPath(p string) (int, string, bool) {
	rest, ok := strings.CutPrefix(p, "extra.")
	if !ok {
		return 0, p, false
	}
	n, rel, _ := strings.Cut(rest, ".")
	var i int
	fmt.Sscan(n, &i)
	return i, rel, true
}

// Every option is either live (IsLive) or needs a restart (RestartNeeded), and the two agree.
func TestLiveListMatchesRestartRule(t *testing.T) {
	base := Default()
	for i := range base.Extra {
		base.Extra[i].Enabled = true // the other channels' options only count while they are on
	}
	for _, f := range Fields(base) {
		if strings.HasPrefix(f.Path, "server.") {
			continue // navtexd restart, not the channel
		}
		base := base
		// an option of a source that is not selected changes nothing, so test each one with its own source selected
		for _, g := range []string{"device", "hpsdr", "stdin", "file"} {
			if strings.HasPrefix(f.Path, "input."+g+".") {
				base.Input.Source = g
			}
			if n, rel, ok := extraPath(f.Path); ok && strings.HasPrefix(rel, "input."+g+".") {
				base.Extra[n].Input.Source = g
			}
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
		_, rel, _ := extraPath(f.Path)
		switch rel {
		case "input.source":
			v = "stdin"
		case "input.stdin.pcm_format":
			v = "f32le"
		case "input.stdin.start_utc", "input.file.start_utc":
			v = "2026-01-01 00:00:00"
		case "input.stdin.channels", "input.device.channels":
			v = "2"
		case "input.hpsdr.sample_rate_hz":
			v = "96000"
		case "meters.spectrum_fft":
			v = "512"
		case "meters.spectrum_smooth", "meters.quality_eye_weight":
			v = "0.5"
		case "output.format":
			v = "json"
		case "segment.fragment_words_vocabulary":
			v = "on"
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

func TestHPSDRInput(t *testing.T) {
	c := Default()
	c.Input.Source = "hpsdr"
	if err := c.Validate(); err != nil {
		t.Fatalf("default rate should suit a radio: %v", err)
	}
	if got := c.LiveCentreHz(); got != c.Channel.FrequencyHz-HPSDRCarrierHz {
		t.Fatalf("LO %v", got)
	}
	c.Input.HPSDR.SampleRateHz = 44100
	if c.Validate() == nil {
		t.Fatal("44100 Hz is not a radio rate")
	}
	c.Input.HPSDR.SampleRateHz = 192000
	c.Input.HPSDR.CentreHz = 517000
	if err := c.Validate(); err != nil || c.LiveCentreHz() != 517000 {
		t.Fatalf("explicit LO: %v %v", err, c.LiveCentreHz())
	}
}

// Each source keeps its own rate, centre, autotune and carrier.
func TestSourcesAreIndependent(t *testing.T) {
	c := Default()
	c.Input.Device.SampleRateHz = 44100
	c.Input.Stdin.SampleRateHz = 96000
	c.Input.HPSDR.SampleRateHz = 192000
	for src, want := range map[string]float64{"device": 44100, "stdin": 96000, "hpsdr": 192000, "file": 0} {
		c.Input.Source = src
		if c.SampleRate() != want {
			t.Errorf("%s: rate %v want %v", src, c.SampleRate(), want)
		}
	}
	c.Input.Source = "hpsdr"
	c.SetTuning(Tuning{CentreHz: 0, Autotune: false, ManualCarrierHz: 2500})
	c.Input.Source = "device"
	if tu := c.Tuning(); !tu.Autotune || tu.ManualCarrierHz != DefaultCarrierHz {
		t.Fatalf("the sound card picked up the radio's carrier: %+v", tu)
	}
	c.Input.Source = "hpsdr"
	if tu := c.Tuning(); tu.Autotune || tu.ManualCarrierHz != 2500 {
		t.Fatalf("radio carrier lost: %+v", tu)
	}
	// changing the carrier of the selected source is live; changing another source's rate is not even a change
	a, b := c, c
	b.Input.Device.SampleRateHz = 22050
	if RestartNeeded(a, b) {
		t.Error("an unselected source's rate restarted the channel")
	}
	b = c
	b.SetTuning(Tuning{ManualCarrierHz: 2600})
	if RestartNeeded(a, b) {
		t.Error("moving the carrier restarted the channel")
	}
}

// A settings file from before the split loads with its meaning kept.
func TestOldInputSettingsMigrate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	old := `{"input":{"source":"hpsdr","file":"C:\\rec\\x.wav","device":"Line 1","sample_rate_hz":192000,"channels":2,
		"pcm_format":"f32le","centre_hz":517000,"radio":"10.1.2.3","swap_iq":true,"realtime":true,"block_size":8192},
		"channel":{"frequency_hz":490000,"autotune":false,"manual_carrier_hz":2750}}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	in := c.Input
	if in.Source != "hpsdr" || in.BlockSize != 8192 || c.Channel.FrequencyHz != 490000 {
		t.Fatalf("common: %+v", in)
	}
	if in.File.Path != `C:\rec\x.wav` || !in.File.Realtime || in.Device.Name != "Line 1" || in.HPSDR.Address != "10.1.2.3" {
		t.Fatalf("names: %+v", in)
	}
	if in.Device.SampleRateHz != 192000 || in.Stdin.SampleRateHz != 192000 || in.HPSDR.SampleRateHz != 192000 || in.Device.Channels != 2 || in.Stdin.PCMFormat != "f32le" {
		t.Fatalf("rates: %+v", in)
	}
	if in.HPSDR.CentreHz != 517000 || !in.HPSDR.SwapIQ || in.HPSDR.Autotune || in.HPSDR.ManualCarrierHz != 2750 {
		t.Fatalf("radio tuning: %+v", in.HPSDR)
	}
	if !in.Device.Autotune || in.Device.ManualCarrierHz != DefaultCarrierHz {
		t.Fatalf("the carrier setting should only follow the selected source: %+v", in.Device)
	}
	// a rate a radio cannot use is not copied to it
	if err := os.WriteFile(p, []byte(`{"input":{"sample_rate_hz":44100}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(p); err != nil || c.Input.HPSDR.SampleRateHz != 48000 || c.Input.Device.SampleRateHz != 44100 {
		t.Fatalf("%v %+v", err, c.Input)
	}
	// a file already in the new layout is untouched
	if err := os.WriteFile(p, c.JSON(), 0o644); err != nil {
		t.Fatal(err)
	}
	if c2, err := Load(p); err != nil || c2 != c {
		t.Fatalf("round trip: %v", err)
	}
}

func TestExtraChannels(t *testing.T) {
	c := Default()
	if c.ActiveChannels() != 1 || c.ChannelEnabled(1) || !c.ChannelEnabled(0) || c.ChannelEnabled(Channels) {
		t.Fatal("only the first channel is on by default")
	}
	if c.Extra[0].Channel.FrequencyHz != 490000 || c.Channel.FrequencyHz != 518000 {
		t.Errorf("default frequencies %v / %v", c.Channel.FrequencyHz, c.Extra[0].Channel.FrequencyHz)
	}
	// ForChannel: the channel's groups stand in for the top-level ones, the shared options stay, Extra is cleared
	c.Extra[0].Channel.FrequencyHz = 424000
	c.Extra[0].Decoder.MinRun = 7
	c.Extra[2].Channel.FrequencyHz = 518000
	c.Operator.Comment = "shared"
	d := c.ForChannel(1)
	if d.Channel.FrequencyHz != 424000 || d.Decoder.MinRun != 7 || d.Operator.Comment != "shared" || d.Extra != ([Channels - 1]Extra{}) {
		t.Errorf("ForChannel(1): %+v", d.Channel)
	}
	if d := c.ForChannel(Channels - 1); d.Channel.FrequencyHz != 518000 || d.Channel.Name != fmt.Sprintf("Channel %d", Channels) {
		t.Errorf("ForChannel(last): %+v", d.Channel)
	}
	if e := c.ForChannel(0); e.Channel.FrequencyHz != 518000 || e.Extra != ([Channels - 1]Extra{}) {
		t.Errorf("ForChannel(0): %+v", e.Channel)
	}
	// EditChannel changes only the named channel
	c.EditChannel(1, func(d *Config) { d.Decoder.UnshiftOnSpace = !d.Decoder.UnshiftOnSpace })
	if c.Decoder.UnshiftOnSpace == c.Extra[0].Decoder.UnshiftOnSpace || c.Extra[0].Decoder.UnshiftOnSpace == c.Extra[1].Decoder.UnshiftOnSpace {
		t.Error("EditChannel(1) changed another channel too (or nothing)")
	}
	c.EditChannel(0, func(d *Config) { d.Channel.FrequencyHz = 490000 })
	if c.Channel.FrequencyHz != 490000 || c.Extra[0].Channel.FrequencyHz != 424000 {
		t.Error("EditChannel(0) went to the wrong channel")
	}
	// labels
	if c.Label(0) != "Channel 1" || c.Label(1) != "Channel 2" || c.Label(3) != "Channel 4" {
		t.Errorf("labels %q %q %q", c.Label(0), c.Label(1), c.Label(3))
	}
	c.Extra[0].Channel.Name = " Loop "
	c.Channel.Name = ""
	if c.Label(0) != "Channel 1" {
		t.Errorf("an empty name falls back to the default: %q", c.Label(0))
	}
	if c.Label(1) != "Loop" {
		t.Errorf("label %q", c.Label(1))
	}
	// a bad extra channel is only an error while it is on
	c.Extra[1].DDC.TargetRateHz = 10
	if err := c.Validate(); err != nil {
		t.Errorf("a disabled channel must not be validated: %v", err)
	}
	c.Extra[1].Enabled = true
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "channel 3") {
		t.Errorf("want a channel 3 error, got %v", err)
	}
	if c.ActiveChannels() != 2 || !c.ChannelEnabled(2) || c.ChannelEnabled(1) {
		t.Errorf("active channels %d", c.ActiveChannels())
	}
}

// Set and Fields reach the channels' options by "extra.<n>.<option>" and a change to one does not disturb the others.
func TestExtraPaths(t *testing.T) {
	c := Default()
	if err := Set(&c, "extra.1.channel.frequency_hz", "424000"); err != nil {
		t.Fatal(err)
	}
	if c.Extra[1].Channel.FrequencyHz != 424000 || c.Extra[0].Channel.FrequencyHz != 490000 || c.Extra[2].Channel.Name != "Channel 4" {
		t.Errorf("Set went wrong: %v %v %q", c.Extra[1].Channel.FrequencyHz, c.Extra[0].Channel.FrequencyHz, c.Extra[2].Channel.Name)
	}
	if err := Set(&c, "extra.2.enabled", "true"); err != nil || !c.Extra[2].Enabled || c.Extra[1].Channel.FrequencyHz != 424000 {
		t.Errorf("enable: %v", err)
	}
	if Set(&c, "extra.9.enabled", "true") == nil {
		t.Error("a channel that does not exist")
	}
	found := false
	for _, f := range Fields(c) {
		if strings.HasPrefix(f.Path, "extra.2.decoder.") {
			found = true
		}
	}
	if !found {
		t.Error("Fields lacks extra.2.decoder.*")
	}
}

// A settings file from the time of one extra channel ("second") keeps its meaning; a short extra list leaves the rest default.
func TestMigrateSecond(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"second":{"enabled":true,"channel":{"frequency_hz":424000,"name":"Japan"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Extra[0].Enabled || c.Extra[0].Channel.FrequencyHz != 424000 || c.Extra[0].Channel.Name != "Japan" {
		t.Errorf("second not carried over: %+v", c.Extra[0].Channel)
	}
	if c.Extra[1].Enabled || c.Extra[1].Input.BlockSize != 16384 || c.Extra[2].Channel.Name != "Channel 4" || c.Extra[1].Decoder.MinRun == 0 {
		t.Errorf("the other channels must keep their defaults: %+v", c.Extra[1])
	}
	if strings.Contains(string(c.JSON()), `"second"`) {
		t.Error("saved settings still have a second group")
	}
	if err := os.WriteFile(p, []byte(`{"extra":[{"channel":{"name":"A"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(p); err != nil || c.Extra[0].Channel.Name != "A" || c.Extra[1].Channel.Name != "Channel 3" {
		t.Errorf("short list: %v %+v", err, c.Extra[1].Channel)
	}
}

// Changing only the second channel must not restart the first, and the reverse.
func TestRestartPerChannel(t *testing.T) {
	a := Default()
	a.Extra[0].Enabled = true
	b := a
	b.Extra[0].Channel.FrequencyHz = 424000
	if RestartNeededFor(a, b, 0) || !RestartNeededFor(a, b, 1) || !RestartNeeded(a, b) {
		t.Error("a frequency change of the second channel")
	}
	b = a
	b.Channel.FrequencyHz = 424000
	if !RestartNeededFor(a, b, 0) || RestartNeededFor(a, b, 1) {
		t.Error("a frequency change of the first channel")
	}
	b = a
	b.Extra[0].Channel.Comment, b.Channel.Name = "note", "name" // live
	if RestartNeeded(a, b) {
		t.Error("names and comments are live")
	}
	b = a
	b.Extra[0].Enabled = false
	if !RestartNeededFor(a, b, 1) || RestartNeededFor(a, b, 0) {
		t.Error("switching the second channel off")
	}
	// while it is off its settings do not matter
	a.Extra[0].Enabled = false
	b = a
	b.Extra[0].Channel.FrequencyHz = 424000
	if RestartNeeded(a, b) {
		t.Error("settings of a disabled channel")
	}
}
