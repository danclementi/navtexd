package iq

import (
	"os"
	"regexp"
	"time"
)

var reStamp = regexp.MustCompile(`(\d{8})[_T-](\d{6})`)

// InferStartTime works out when a recording started: from a yyyymmdd_hhmmss stamp in the file name (SDRconnect names
// files this way; the stamp is taken as local time), otherwise from the file's modification time minus its duration.
// The result is always UTC. The second result says which source was used.
func InferStartTime(path string, durationS float64) (time.Time, string, error) {
	name := path
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			name = path[i+1:]
			break
		}
	}
	if m := reStamp.FindStringSubmatch(name); m != nil {
		if t, err := time.ParseInLocation("20060102 150405", m[1]+" "+m[2], time.Local); err == nil {
			return t.UTC(), "file name (local time converted to UTC)", nil
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}, "", err
	}
	return st.ModTime().Add(-time.Duration(durationS * float64(time.Second))).UTC(), "file modification time minus duration", nil
}
