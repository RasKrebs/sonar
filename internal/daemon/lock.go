package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/raskrebs/sonar/internal/runs"
)

// ErrAlreadyRunning is returned by AcquireLock when another daemon holds the
// lock. Its message carries the holder's pid when the lock file could be read.
type ErrAlreadyRunning struct {
	PID  int
	Path string
}

func (e *ErrAlreadyRunning) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("daemon already running (pid %d)", e.PID)
	}
	return "daemon already running"
}

// IsAlreadyRunning reports whether err is (or wraps) ErrAlreadyRunning.
func IsAlreadyRunning(err error) bool {
	var e *ErrAlreadyRunning
	return errors.As(err, &e)
}

// Lock is the single-instance lock: an advisory lock (flock on Unix,
// LockFileEx on Windows) on <socket dir>/daemon.lock, whose contents are the
// holder's pid so a second `sonar serve` can name the process it lost to.
//
// Unix locks the whole file; Windows locks one byte far past the end of it,
// because a Windows lock also blocks reads of the range it covers and the pid
// has to stay readable while the lock is held (see lock_windows.go).
type Lock struct {
	path string
	f    *os.File
}

// AcquireLock takes the lock without blocking. It returns ErrAlreadyRunning
// when another process holds it.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if isLockedOpenError(err) {
			// Windows can refuse the open itself while another process has the
			// file locked or opened without sharing, where Unix would let us
			// open it and only refuse the lock. It is the same answer —
			// somebody else has it — so it gets the same error, and callers
			// that retry (WaitForLockRelease) keep retrying.
			return nil, &ErrAlreadyRunning{PID: LockHolderPID(path), Path: path}
		}
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	// A daemon spawns children (`sonar start`, `sonar up`, autostart). None of
	// them may inherit the lock handle: an inherited handle keeps the lock —
	// and, on Windows, the file itself — alive after the daemon that took it
	// has exited.
	markNotInheritable(f)

	if err := lockFile(f); err != nil {
		pid := readLockPID(f)
		f.Close()
		if errors.Is(err, errLockBusy) {
			return nil, &ErrAlreadyRunning{PID: pid, Path: path}
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	// The lock is ours: replace whatever pid was in there with our own.
	if err := f.Truncate(0); err == nil {
		if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
			unlockFile(f)
			f.Close()
			return nil, fmt.Errorf("writing lock file: %w", err)
		}
		_ = f.Sync()
	}
	return &Lock{path: path, f: f}, nil
}

// Release drops the lock and removes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	l.f.Close()
	l.f = nil
	_ = os.Remove(l.path)
	return err
}

// Path is the lock file's path.
func (l *Lock) Path() string { return l.path }

// WaitForLockRelease blocks until the daemon lock at path is free, then
// returns. It is what `sonar daemon restart` waits on: a daemon closes its
// socket well before it releases the lock, so waiting for the socket to stop
// accepting is not enough — the replacement daemon would start, fail to take
// the lock, exit "already running", and leave nothing running (contract §21).
//
// The lock is taken and immediately released, which is the only honest test
// that it is free. A lock whose recorded holder is gone is also treated as
// free, so a stale lock file cannot make restart hang for the whole timeout.
//
// On Windows the wait has to survive one more failure mode: opening a file the
// outgoing daemon still holds fails outright (ERROR_LOCK_VIOLATION or
// ERROR_SHARING_VIOLATION) rather than blocking. AcquireLock reports those as
// ErrAlreadyRunning, so they are retried here instead of aborting the restart.
func WaitForLockRelease(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		lock, err := AcquireLock(path)
		if err == nil {
			_ = lock.Release()
			return nil
		}
		if !IsAlreadyRunning(err) {
			// Anything other than "someone holds it" (an unreadable directory,
			// say) will not resolve itself by waiting.
			return err
		}
		last = err
		var already *ErrAlreadyRunning
		if errors.As(err, &already) && already.PID > 0 && !runs.PIDAlive(already.PID) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the previous daemon still holds %s after %s: %w", path, timeout, last)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// LockHolderPID reads the pid recorded in a lock file. It returns 0 when the
// file is missing or unreadable; it does not prove the process is alive.
func LockHolderPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func readLockPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	if n <= 0 {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		return 0
	}
	return pid
}
