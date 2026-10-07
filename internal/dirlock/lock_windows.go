//go:build windows

package dirlock

import "syscall"

type handle = syscall.Handle

// acquire opens the file with no sharing at all; a second open fails with a sharing violation.
func acquire(path string) (handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && (errno == 32 || errno == 33) { // sharing / lock violation
			return 0, ErrBusy
		}
		return 0, err
	}
	return h, nil
}

func release(h handle) { syscall.CloseHandle(h) }
