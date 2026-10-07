// Package config holds every user-adjustable option in one place. The whole tree is plain data with `json` names and
// `doc`, `unit` and `choices` struct tags, so it can be saved to / loaded from a JSON file today and turned into a
// settings dialog by a GUI later (see Fields, which lists every option with its path, value, help text and choices).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"navtex/internal/fsk"
	"navtex/internal/iq"
	"navtex/internal/segment"
	"navtex/internal/sitor"
)

// TimeFormat is the layout of every time shown to the user. All times are UTC.
const TimeFormat = "2006-01-02 15:04:05"

// Config is the complete option set.
type Config struct {
	Input       Input              `json:"input"`
	Channel     Channel            `json:"channel"`
	DDC         DDC                `json:"ddc"`
	Receiver    fsk.ReceiverConfig `json:"receiver"`
	Decoder     sitor.Config       `json:"decoder"`
	Segment     segment.Config     `json:"segment"`
	Meters      Meters             `json:"meters"`
	Operator    Operator           `json:"operator"`
	Logging     Logging            `json:"logging"`
	Autocorrect Autocorrect        `json:"autocorrect"`
	Server      Server             `json:"server"`
	Clips       Clips              `json:"clips"`
	Output      Output             `json:"output"`
}

// Clips controls the "Save audio clip" function: the last seconds of input are kept in memory.
type Clips struct {
	BeforeS float64 `json:"before_s" unit:"s" doc:"Input kept in memory and saved when the clip button is pressed: this many seconds before the press (0 = clip function off)"`
	AfterS  float64 `json:"after_s" unit:"s" doc:"The clip continues this many seconds after the press, so that the end of an event is included"`
	Dir     string  `json:"dir" doc:"Folder for saved clips (WAV plus a .json file with the settings in force); relative to the data folder"`
}

// Meters controls the live displays fed by the engine.
type Meters struct {
	RateHz           float64 `json:"rate_hz" unit:"Hz" doc:"How often level, quality and status figures are reported (and the spectrum, at most this often)"`
	SpectrumMaxHz    float64 `json:"spectrum_max_hz" unit:"Hz" doc:"Highest audio frequency shown in the spectrum (real audio input)"`
	SpectrumFFT      int     `json:"spectrum_fft" doc:"Spectrum FFT length, a power of two (0 = automatic, about 6 Hz per bin)"`
	SpectrumSmooth   float64 `json:"spectrum_smooth" doc:"Smoothing of the filtered spectrum, 0 to 0.99: filtered = a*old + (1-a)*new (power, per frame), with a = this value; 0 = no smoothing"`
	SpectrumRaw      bool    `json:"spectrum_show_unfiltered" doc:"Show the unfiltered (unsmoothed) spectrum trace behind the filtered one"`
	QualityWindowS   float64 `json:"quality_window_s" unit:"s" doc:"Length of signal analysed for each signal-quality value"`
	QualityNoiseCF   float64 `json:"quality_noise_crest" doc:"Crest factor of noise that counts as quality 0"`
	QualityGoodCF    float64 `json:"quality_clean_crest" doc:"Crest factor of a clean signal, quality 100 (a pure tone is 2.83)"`
	LevelTauS        float64 `json:"level_display_tau_s" unit:"s" doc:"Time constant of the first-order low-pass (applied in dB) that smooths the input level gauge in the browser (0 = no smoothing); the clip indicator is not smoothed"`
	QualityEyeWeight float64 `json:"quality_eye_weight" doc:"Share of the symbol-decision (eye) measure in the quality figure, 0 to 1; the rest is the crest-factor measure. The eye measure rates the last 2 s of locked symbols and is only used while the decoder is locked"`
	QualityEyeLowDB  float64 `json:"quality_eye_low_db" unit:"dB" doc:"Decision signal-to-noise ratio that counts as quality 0 in the eye measure (3 dB = about 8% symbol errors)"`
	QualityEyeHighDB float64 `json:"quality_eye_high_db" unit:"dB" doc:"Decision signal-to-noise ratio that counts as quality 100 in the eye measure (15 dB = practically error free)"`
	QualityTauS      float64 `json:"quality_display_tau_s" unit:"s" doc:"Time constant of the first-order low-pass that smooths the quality gauge in the browser (0 = no smoothing); stored qualities are not smoothed"`
}

// Operator holds the operator's own information.
type Operator struct {
	Comment string  `json:"comment" doc:"Operational comment stored with every record, e.g. the antenna in use"`
	LatDeg  float64 `json:"latitude_deg" unit:"deg" doc:"Receiving position, latitude, north positive (0 with longitude 0 = not set). Used for distances, bearings and sun times; never sent to viewers"`
	LonDeg  float64 `json:"longitude_deg" unit:"deg" doc:"Receiving position, longitude, east positive"`
	Info    string  `json:"info" doc:"Personal and equipment information (name, receiver, antenna) for reception reports"`
	Public  bool    `json:"public_location_info" doc:"Let view-only (remote) users see location-based information: sunrise/sunset, distances and bearings in the Log, Schedule and Report tabs, and the Ephemeris tab. These reveal roughly where the receiver is, so the default is off"`
}

// Logging controls what the displays show about stored text.
type Logging struct {
	LineQuality bool `json:"line_quality" doc:"Show the signal quality figure (NN>) in front of each text line in the live text and the Log"`
}

// Autocorrect controls the repair of words that contain unreadable characters (shown in [brackets] in the Log).
type Autocorrect struct {
	Enabled       bool   `json:"enabled" doc:"Correct words with unreadable characters from the maritime vocabulary; the result is stored beside the raw text, which is never changed"`
	MinLen        int    `json:"min_word_letters" unit:"letters" doc:"Shortest word that is looked up"`
	MaxLen        int    `json:"max_word_letters" unit:"letters" doc:"Longest word that is looked up"`
	MaxUnreadable int    `json:"max_unreadable_percent" unit:"%" doc:"Words with a larger share of unreadable characters are left alone"`
	MaxCandidates int    `json:"max_candidates" doc:"Show up to this many possible words as [A|B|C]; a word that fits more vocabulary words is left alone"`
	WordsFile     string `json:"extra_words_file" doc:"Text file (one word per line) with more words for the vocabulary; relative names are in the data directory. Read again when it changes"`
}

// Server configures the network server (changes need a restart of navtexd).
type Server struct {
	Listen        string `json:"listen" doc:"Address of the web server, e.g. 127.0.0.1:8080 (this computer only) or :8080 (all interfaces). off = no web server"`
	OpenBrowser   bool   `json:"open_browser" doc:"Open the web interface in the default browser when the program starts (takes effect at the next start)"`
	TunnelListen  string `json:"tunnel_listen" doc:"Extra address for a reverse tunnel (e.g. cloudflared). Everything arriving here is a viewer, whatever its address. Empty = none"`
	AdminNetworks string `json:"admin_networks" doc:"Extra networks, comma separated (e.g. 100.64.0.0/10), whose clients may change settings. Loopback and private LAN ranges always may"`
	Hosts         string `json:"hosts" doc:"Extra host names, comma separated, under which the server is reached from the local network (settings are refused for unknown host names)"`
	Token         string `json:"token" doc:"If set, changing settings also needs this access token (entered once in the browser)"`
	ViewerHints   bool   `json:"viewer_position_hints" hidden:"true" doc:"Obsolete: now operator.public_location_info (an old setting is carried over when the file is loaded)"`
	GeoLookup     bool   `json:"geo_lookup" doc:"Console messages about connecting browsers include a location. Behind a Cloudflare tunnel its country (and city, if enabled) headers are used; for other public addresses the city is looked up on the internet (ipwho.is), which sends that address to that service. Off = no lookup, and local addresses are only labelled as local"`
	PublicHosts   string `json:"public_hosts" doc:"Host names of the public (tunnel) address, comma separated; requests for them are always viewers"`
}

// Input describes where the samples come from.
type Input struct {
	Source       string  `json:"source" doc:"Where the samples come from: file (WAV recording), device (sound card / virtual audio cable input, live) or stdin (raw PCM piped in)" choices:"file,device,stdin"`
	File         string  `json:"file" doc:"WAV recording to decode (mono = real audio, stereo = I/Q); used when source is file"`
	Device       string  `json:"device" doc:"Audio input for source=device: part of its name (see -list-devices). Empty = the system default input"`
	SampleRateHz float64 `json:"sample_rate_hz" unit:"Hz" doc:"Sample rate of a device or stdin stream (WAV files carry their own)"`
	Channels     int     `json:"channels" doc:"Device / stdin: 1 = real audio, 2 = I/Q"`
	PCMFormat    string  `json:"pcm_format" doc:"Sample encoding of a stdin stream" choices:"s16le,f32le"`
	CentreHz     float64 `json:"centre_hz" unit:"Hz" doc:"I/Q: centre frequency of the recording. Real audio: the receiver's dial (LO) frequency. 0 = from the file name; for device/stdin: the channel frequency minus 1000 Hz, i.e. the carrier is expected near 1000 Hz of audio"`
	SwapIQ       bool    `json:"swap_iq" doc:"Two-channel input: left channel is Q and right is I"`
	StartUTC     string  `json:"start_utc" doc:"Time of the first sample, UTC, yyyy-mm-dd hh:mm:ss. Empty = infer from the file name (taken as this computer's local time) or the file's modification time; for device / stdin, empty = use the system clock"`
	BlockSize    int     `json:"block_size" unit:"samples" doc:"Samples read from the source at a time"`
	Realtime     bool    `json:"realtime" doc:"File input: play the recording back at its real speed instead of as fast as possible (for testing displays)"`
}

// Channel selects the NAVTEX frequency to decode.
type Channel struct {
	FrequencyHz     float64 `json:"frequency_hz" unit:"Hz" doc:"RF frequency of the NAVTEX channel: 518000 (international), 490000 (local language) or 424000 (Japan)"`
	Autotune        bool    `json:"autotune" doc:"Find and follow the carrier automatically. Off: the carrier stays at manual_carrier_hz"`
	ManualCarrierHz float64 `json:"manual_carrier_hz" unit:"Hz" doc:"Audio frequency of the carrier (centre between mark and space) used when autotune is off (the carrier marker in the spectrum can be dragged to set it)"`
}

// DDC controls the digital down-converter.
type DDC struct {
	TargetRateHz float64 `json:"target_rate_hz" unit:"Hz" doc:"Approximate sample rate after down-conversion (about 40 samples per symbol)"`
}

// Output controls what is printed.
type Output struct {
	Format             string `json:"format" doc:"text: time-stamped decoded text; messages: parsed messages; json: one JSON object per message" choices:"text,messages,json"`
	Verbose            bool   `json:"verbose" doc:"Also report carrier, framing and bit-order changes on stderr"`
	IncompleteMessages bool   `json:"incomplete_messages" doc:"Also report messages that were cut short (no NNNN before the next ZCZC or the end of the stream)"`
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		Input:       Input{Source: "device", SampleRateHz: 48000, Channels: 1, PCMFormat: "s16le", BlockSize: 16384},
		Channel:     Channel{FrequencyHz: 518000, Autotune: true, ManualCarrierHz: DefaultCarrierHz},
		DDC:         DDC{TargetRateHz: 4000},
		Receiver:    fsk.DefaultReceiverConfig(0),
		Decoder:     sitor.DefaultConfig(),
		Segment:     segment.DefaultConfig(),
		Autocorrect: Autocorrect{Enabled: true, MinLen: 5, MaxLen: 16, MaxUnreadable: 50, MaxCandidates: 4, WordsFile: "vocabulary.txt"},
		Meters: Meters{RateHz: 10, SpectrumMaxHz: 2700, SpectrumSmooth: 0.85, SpectrumRaw: true, LevelTauS: 0.5, QualityWindowS: 0.5,
			QualityNoiseCF: 5.0, QualityGoodCF: 2.83, QualityTauS: 1, QualityEyeWeight: 0.8, QualityEyeLowDB: 3, QualityEyeHighDB: 15},
		Logging: Logging{LineQuality: true},
		Clips:   Clips{BeforeS: 60, AfterS: 10, Dir: "clips"},
		Server:  Server{Listen: "127.0.0.1:8080", OpenBrowser: true},
		Output:  Output{Format: "text", IncompleteMessages: true},
	}
}

// Validate checks the values and returns all problems found.
func (c Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if c.Channel.FrequencyHz <= 0 {
		bad("channel.frequency_hz must be positive")
	}
	if c.DDC.TargetRateHz < 1000 {
		bad("ddc.target_rate_hz must be at least 1000")
	}
	switch c.Input.Source {
	case "file", "device", "stdin":
	default:
		bad("input.source %q: want file, device or stdin", c.Input.Source)
	}
	if c.Input.Source != "file" {
		if c.Input.SampleRateHz < 8000 || c.Input.SampleRateHz > 400000 {
			bad("input.sample_rate_hz must be between 8000 and 400000")
		}
		if c.Input.Channels != 1 && c.Input.Channels != 2 {
			bad("input.channels must be 1 (real audio) or 2 (I/Q)")
		}
		if _, err := iq.ParsePCMFormat(c.Input.PCMFormat); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Clips.BeforeS < 0 || c.Clips.AfterS < 0 || c.Clips.BeforeS+c.Clips.AfterS > 600 {
		bad("clips.before_s and clips.after_s must be positive, together at most 600 s")
	}
	if c.Input.BlockSize < 64 {
		bad("input.block_size must be at least 64")
	}
	if _, err := ParseStart(c.Input.StartUTC); err != nil {
		errs = append(errs, err)
	}
	switch c.Output.Format {
	case "text", "messages", "json":
	default:
		bad("output.format %q: want text, messages or json", c.Output.Format)
	}
	r := c.Receiver
	if r.Baud <= 0 || r.ShiftHz <= 0 || r.NarrowCutoff <= 0 {
		bad("receiver.demod: baud, shift_hz and channel_cutoff_hz must be positive")
	}
	if r.BlkSyms < 10 {
		bad("receiver.demod.timing_block_symbols must be at least 10")
	}
	if r.SearchWinS < 1 || r.SearchHopS <= 0 || r.SearchHz <= 0 {
		bad("receiver search settings must be positive (search_window_s >= 1)")
	}
	if r.TrackWindow < r.TrackMinTotal || r.TrackMinTotal < 2*r.TrackMinEach {
		bad("receiver tracking: need track_window_symbols >= track_min_symbols >= 2 * track_min_each_sign")
	}
	if a := c.Autocorrect; a.MinLen < 3 || a.MaxLen < a.MinLen || a.MaxLen > 40 || a.MaxUnreadable < 0 || a.MaxUnreadable > 90 || a.MaxCandidates < 1 || a.MaxCandidates > 10 {
		bad("autocorrect: word letters 3..40 (min <= max), unreadable 0..90 %, candidates 1..10")
	}
	m := c.Meters
	if m.RateHz < 1 || m.RateHz > 50 || m.SpectrumMaxHz < 500 || m.SpectrumSmooth < 0 || m.SpectrumSmooth > 0.99 || m.LevelTauS < 0 ||
		m.QualityWindowS < 0.1 || m.QualityTauS < 0 || m.QualityNoiseCF <= m.QualityGoodCF || m.QualityEyeWeight < 0 || m.QualityEyeWeight > 1 || m.QualityEyeHighDB <= m.QualityEyeLowDB || (m.SpectrumFFT != 0 && (m.SpectrumFFT < 256 || m.SpectrumFFT&(m.SpectrumFFT-1) != 0)) {
		bad("meters: rate 1..50 Hz, spectrum_max_hz >= 500, spectrum_smooth in [0,0.99], level/quality display time constants >= 0, quality window >= 0.1 s, noise crest > clean crest, eye weight 0..1 with eye high dB > low dB, FFT a power of two >= 256")
	}
	if c.Segment.IdleCloseS < 5 || c.Segment.MinFragmentChars < 0 {
		bad("segment: idle_close_s must be at least 5 and min_fragment_chars not negative")
	}
	d := c.Decoder
	if d.MinRun < 2 || d.MaxBad < 1 || d.VoteAgree < 1 || d.VoteFactor < 1 || d.ShiftHz <= 0 {
		bad("decoder settings out of range")
	}
	return errors.Join(errs...)
}

// ParseStart parses a start time in UTC; the empty string means "not set".
func ParseStart(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation(TimeFormat, strings.TrimSpace(s), time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("input.start_utc %q: want yyyy-mm-dd hh:mm:ss (UTC)", s)
	}
	return t, nil
}

// StartTime resolves the UTC time of the first sample of a recording: the configured start, else inferred from the
// file. The second result says where it came from.
func (c Config) StartTime(durationS float64) (time.Time, string, error) {
	if t, err := ParseStart(c.Input.StartUTC); err != nil || !t.IsZero() {
		return t, "configuration", err
	}
	return iq.InferStartTime(c.Input.File, durationS)
}

// Load reads a JSON file on top of the defaults (missing keys keep their default; unknown keys are an error).
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	c := Default()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.Server.ViewerHints { // the option moved from the network settings to the station settings
		c.Operator.Public, c.Server.ViewerHints = true, false
	}
	return c, c.Validate()
}

// JSON renders the configuration as indented JSON.
func (c Config) JSON() []byte {
	b, _ := json.MarshalIndent(c, "", "  ")
	return append(b, '\n')
}

// Save writes the configuration to a JSON file.
func (c Config) Save(path string) error { return os.WriteFile(path, c.JSON(), 0o644) }

// Field describes one option, for generating settings screens and help.
type Field struct {
	Path    string   // e.g. "receiver.blanker_threshold"
	Kind    string   // bool, int, float, string, enum
	Value   any      // current value (enums as their text)
	Doc     string   // one-line help
	Unit    string   // may be empty
	Choices []string // for enums
}

// Fields lists every option of c in declaration order.
func Fields(c Config) []Field {
	var out []Field
	walk(reflect.ValueOf(c), "", &out)
	return out
}

func walk(v reflect.Value, prefix string, out *[]Field) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		name := strings.Split(sf.Tag.Get("json"), ",")[0]
		if name == "-" || !sf.IsExported() || sf.Tag.Get("hidden") == "true" {
			continue
		}
		if name == "" {
			name = strings.ToLower(sf.Name)
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			walk(fv, path, out)
			continue
		}
		f := Field{Path: path, Doc: sf.Tag.Get("doc"), Unit: sf.Tag.Get("unit")}
		if ch := sf.Tag.Get("choices"); ch != "" {
			f.Choices = strings.Split(ch, ",")
		}
		tm, isText := fv.Interface().(interface{ MarshalText() ([]byte, error) })
		switch {
		case isText && fv.Kind() != reflect.String: // named integer types with names (bit order, polarity ...): an enum
			b, _ := tm.MarshalText()
			f.Kind, f.Value = "enum", string(b)
			*out = append(*out, f)
			continue
		}
		switch fv.Kind() {
		case reflect.Bool:
			f.Kind, f.Value = "bool", fv.Bool()
		case reflect.Int, reflect.Int64:
			f.Kind, f.Value = "int", fv.Int()
		case reflect.Float64:
			f.Kind, f.Value = "float", fv.Float()
		case reflect.String:
			f.Kind, f.Value = "string", fv.String()
		default:
			if tm, ok := fv.Interface().(interface{ MarshalText() ([]byte, error) }); ok {
				b, _ := tm.MarshalText()
				f.Kind, f.Value = "enum", string(b)
			} else {
				f.Kind, f.Value = fv.Kind().String(), fv.Interface()
			}
		}
		if f.Kind == "enum" && f.Choices == nil {
			f.Choices = nil
		}
		*out = append(*out, f)
	}
}

// Set changes one option by its Fields path, e.g. Set(&c, "receiver.blanker_threshold", "0") or
// Set(&c, "decoder.unreadable", "star"). Numbers and booleans are read as JSON, anything else as text.
func Set(c *Config, path, value string) error {
	parts := strings.Split(path, ".")
	raw := strings.TrimSpace(value)
	if !json.Valid([]byte(raw)) {
		q, _ := json.Marshal(value)
		raw = string(q)
	}
	doc := raw
	for i := len(parts) - 1; i >= 0; i-- {
		doc = fmt.Sprintf("{%q:%s}", parts[i], doc)
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.DisallowUnknownFields()
	next := *c
	if err := dec.Decode(&next); err != nil {
		return fmt.Errorf("cannot set %s=%s: %w", path, value, err)
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*c = next
	return nil
}

// LiveCentreHz is the centre / LO frequency to use for a device or stdin stream: the configured one, else the channel
// frequency minus DefaultCarrierHz.
func (c Config) LiveCentreHz() float64 {
	if c.Input.CentreHz != 0 {
		return c.Input.CentreHz
	}
	return c.Channel.FrequencyHz - DefaultCarrierHz
}

// DefaultCarrierHz is the audio frequency assumed for the NAVTEX carrier of a live stream when no LO is configured.
const DefaultCarrierHz = 1000.0

// liveFields lists the options (paths, or prefixes ending in ".") that can be applied to a running channel; see
// RestartNeeded. The tests check that this list and RestartNeeded agree for every option.
var liveFields = []string{
	"operator.", "logging.", "autocorrect.", "meters.", "segment.", "decoder.", "channel.autotune", "channel.manual_carrier_hz",
	"receiver.demod.presence_db", "receiver.demod.presence_offset_hz", "receiver.demod.timing_gain",
	"receiver.demod.timing_confidence", "receiver.demod.timing_lost_blocks",
	"receiver.blanker_threshold", "receiver.search_", "receiver.lose_carrier_after_s", "receiver.track_",
}

// IsLive reports whether the option at path takes effect on a running channel without a restart (switching the impulse
// blanker on or off is the one exception inside a live option; RestartNeeded decides that). Server options are not
// live either: they need navtexd restarted.
func IsLive(path string) bool {
	if path == "decoder.shift_hz" {
		return false
	}
	for _, f := range liveFields {
		if path == f || (strings.HasSuffix(f, ".") || strings.HasSuffix(f, "_")) && strings.HasPrefix(path, f) {
			return true
		}
	}
	return false
}

// RestartNeeded reports whether going from configuration a to b needs the channel restarted. Everything that can be
// applied to a running decoder without losing more than a moment of decoding is left out: the display and meter
// options, logging, segmentation, the decoder's decision options, carrier search/tracking, the blanker threshold and
// the demodulator's presence/timing thresholds, autotune and the manual carrier, and the operator information.
// Server settings need a restart of navtexd, not of the channel. What remains is the input, the channel frequency,
// the down-converter, the baud rate, the tone spacing, the channel filter and timing block length, and switching the
// impulse blanker on or off.
func RestartNeeded(a, b Config) bool {
	neutral := func(c *Config) {
		c.Operator, c.Server, c.Meters, c.Logging, c.Segment = Operator{}, Server{}, Meters{}, Logging{}, segment.Config{}
		c.Autocorrect = Autocorrect{}
		c.Channel.Autotune, c.Channel.ManualCarrierHz = false, 0
		c.Decoder = sitor.Config{ShiftHz: c.Decoder.ShiftHz}
		r := &c.Receiver
		blank := r.BlankK > 0
		r.BlankK, r.SearchHz, r.SearchWinS, r.SearchHopS, r.MinContrast, r.LoseAfterS, r.VerifyS = 0, 0, 0, 0, 0, 0, 0
		r.TrackWindow, r.TrackMinEach, r.TrackMinTotal, r.TrackInterval, r.TrackGain, r.TrackMaxStep, r.TrackMaxExcur = 0, 0, 0, 0, 0, 0, 0
		r.PresentDB, r.PresentOff, r.Alpha, r.ConfThr, r.MaxLost = 0, 0, 0, 0, 0
		if blank {
			r.BlankK = 1 // only whether blanking is on matters
		}
	}
	neutral(&a)
	neutral(&b)
	return a != b
}
