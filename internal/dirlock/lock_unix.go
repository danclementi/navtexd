//go:build !windows

package dirlock

import (
	"os"
	"syscall"
)

type handle = *os.File

func acquire(path string) (handle, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

func release(f handle) { f.Close() }
