//go:build windows

package clip

import (
	"syscall"
	"unsafe"
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys, AvailPhys uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// Available reports the free physical memory; ok is false when it cannot be determined.
func Available() (bytes int64, ok bool) {
	p := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, _ := p.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, false
	}
	return int64(min(m.AvailPhys, 1<<62)), true
}
