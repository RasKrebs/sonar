package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Lock is the single-instance lock. It is an advisory whole-file lock (flock on
// Unix, LockFileEx on Windows) on <socket dir>/daemon.lock whose contents are
// the holder's pid, so a second `sonar serve` can name the process it lost to.
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
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
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
