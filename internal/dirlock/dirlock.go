// Package dirlock stops two copies of the program from using the same data folder. The lock is held by the operating
// system for as long as the process lives, so it disappears by itself if the program is killed or crashes.
package dirlock

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ErrBusy means another process holds the lock.
var ErrBusy = errors.New("data folder is in use by another copy of the program")

// File is the name of the lock file inside the data folder.
const File = "navtexd.lock"

// Lock is a held lock; Release gives it up (also done by the system at exit).
type Lock struct{ h handle }

// Acquire locks the data folder dir (which must exist). It returns an error wrapping ErrBusy if it is already locked.
func Acquire(dir string) (*Lock, error) {
	h, err := acquire(filepath.Join(dir, File))
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, fmt.Errorf("%s: %w", dir, ErrBusy)
		}
		return nil, err
	}
	return &Lock{h}, nil
}

// Release unlocks the folder.
func (l *Lock) Release() {
	if l != nil {
		release(l.h)
	}
}
