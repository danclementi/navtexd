//go:build windows

package sysinfo

import (
	"syscall"
	"unsafe"
)

var getSystemTimes = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemTimes")

// systemTimes returns the busy and total CPU time (100 ns units, all cores) of the whole machine so far.
func systemTimes() (busy, total uint64, err error) {
	var idle, kernel, user syscall.Filetime
	r, _, e := getSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, 0, e
	}
	ft := func(f syscall.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	total = ft(kernel) + ft(user) // the kernel time includes the idle time
	return total - ft(idle), total, nil
}
