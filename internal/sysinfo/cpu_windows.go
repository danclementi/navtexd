//go:build windows

package sysinfo

import (
	"syscall"
	"time"
)

func processCPU() (time.Duration, error) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, err
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return time.Duration(kernel.Nanoseconds() + user.Nanoseconds()), nil
}
