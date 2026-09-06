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

// The lock is a single byte at offset 1<<62, past any byte the lock file will
// ever hold.
//
// LockFileEx makes the range it locks unreadable to every other process, so
// locking the whole file hid the one thing the file exists to publish: with a
// daemon running, LockHolderPID and ErrAlreadyRunning.PID both came back 0 and
// os.ReadFile failed with "another process has locked a portion of the file".
// A range that cannot overlap the pid keeps the semantics we want from flock —
// the lock lives on the open handle and dies with it — and leaves offset 0
// readable to anyone who wants to know whose daemon is running.
const (
	lockOffsetLow  = 0
	lockOffsetHigh = 1 << 30 // (1<<30)<<32 == byte 1<<62 of the file
	lockBytesLow   = 1
	lockBytesHigh  = 0
)

// lockRange is the region LockFileEx and UnlockFileEx both address. The offset
// travels in the OVERLAPPED, the length in the byte-count arguments.
func lockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: lockOffsetLow, OffsetHigh: lockOffsetHigh}
}

func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockBytesLow, lockBytesHigh, lockRange())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errLockBusy
	}
	return err
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0,
		lockBytesLow, lockBytesHigh, lockRange())
}

// isLockedOpenError reports whether opening the lock file failed because
// another process has it locked or open without sharing. Windows refuses the
// open where Unix would refuse only the lock, and a restart has to read that
// as "still held, try again" rather than as a broken lock directory.
func isLockedOpenError(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
