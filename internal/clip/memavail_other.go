//go:build !linux && !windows

package clip

// Available is not known on this system.
func Available() (bytes int64, ok bool) { return 0, false }
