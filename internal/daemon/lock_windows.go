//go:build windows

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// errLockBusy marks the "another process holds it" case so AcquireLock can
// turn it into ErrAlreadyRunning.
var errLockBusy = errors.New("lock held by another process")

// lockWholeFile locks a range large enough to cover any lock file we write.
const lockLow, lockHigh = ^uint32(0), ^uint32(0)

func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockLow, lockHigh, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errLockBusy
	}
	return err
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockLow, lockHigh, &overlapped)
}
