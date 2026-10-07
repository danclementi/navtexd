// Package capture opens a sound-card (or virtual audio cable) input as a stream of 16-bit little-endian PCM.
//
// Windows uses the multimedia (MME/waveIn) API directly through system calls, so no C compiler is needed; every
// virtual audio cable and sound card offers an MME input. Other systems run PulseAudio's parec, else ALSA's arecord,
// as a child process. Both are pure Go.
package capture

// Device names an audio input.
type Device struct {
	Name string // the text to put in input.device (a substring is enough on Windows)
	Note string
}

// Open starts capturing the named input (empty = the system default) at rate Hz with the given number of channels and
// returns raw s16le PCM. Close stops it.
func Open(name string, rate float64, channels int) (ReadCloser, error) {
	return open(name, rate, channels)
}

// List returns the available audio inputs.
func List() ([]Device, error) { return list() }

// ReadCloser is io.ReadCloser (declared here to keep the platform files small).
type ReadCloser interface {
	Read(p []byte) (int, error)
	Close() error
}
