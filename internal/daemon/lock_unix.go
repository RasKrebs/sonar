//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// errLockBusy marks the "another process holds it" case so AcquireLock can
// turn it into ErrAlreadyRunning.
var errLockBusy = errors.New("lock held by another process")

func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errLockBusy
	}
	return err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
