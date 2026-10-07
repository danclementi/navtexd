//go:build !windows

package sysinfo

import (
	"syscall"
	"time"
)

// processCPU returns the CPU time used by this process so far.
func processCPU() (time.Duration, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, err
	}
	return time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano()), nil
}
