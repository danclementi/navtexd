//go:build linux

package clip

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Available reports the memory that can be used without swapping (MemAvailable in /proc/meminfo); ok is false when
// it cannot be determined.
func Available() (bytes int64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rest, found := strings.CutPrefix(sc.Text(), "MemAvailable:")
		if !found {
			continue
		}
		fl := strings.Fields(rest) // "123456 kB"
		if len(fl) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fl[0], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb << 10, true
	}
	return 0, false
}
