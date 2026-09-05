package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAcquireLockRefusesASecondInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")

	first, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	defer first.Release()

	// Both flock and LockFileEx associate the lock with the open file
	// description, not the process, so a second AcquireLock is refused even
	// from inside this test binary.
	second, err := AcquireLock(path)
	if err == nil {
		second.Release()
		t.Fatal("a second AcquireLock succeeded; the single-instance lock does not hold")
	}
	if !IsAlreadyRunning(err) {
		t.Fatalf("second AcquireLock returned %v, want ErrAlreadyRunning", err)
	}
	var already *ErrAlreadyRunning
	if errors.As(err, &already) && already.PID != os.Getpid() {
		t.Errorf("ErrAlreadyRunning.PID = %d, want the holder %d", already.PID, os.Getpid())
	}

	// The lock file records the holder's pid so `sonar serve` can name the
	// process it lost to.
	if got := LockHolderPID(path); got != os.Getpid() {
		t.Errorf("LockHolderPID = %d, want %d", got, os.Getpid())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	if strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file holds %q, want the pid %d", data, os.Getpid())
	}
}

func TestReleaseRemovesTheLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file still present after Release: %v", err)
	}

	// A released lock can be taken again.
	again, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("re-acquiring a released lock: %v", err)
	}
	again.Release()
}

func TestLockHolderPIDOnMissingFile(t *testing.T) {
	if got := LockHolderPID(filepath.Join(t.TempDir(), "absent.lock")); got != 0 {
		t.Errorf("LockHolderPID on a missing file = %d, want 0", got)
	}
}

func TestErrAlreadyRunningMessage(t *testing.T) {
	err := &ErrAlreadyRunning{PID: 4321}
	if got, want := err.Error(), "daemon already running (pid 4321)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !IsAlreadyRunning(err) {
		t.Error("IsAlreadyRunning did not recognise its own error type")
	}
	if IsAlreadyRunning(os.ErrNotExist) {
		t.Error("IsAlreadyRunning matched an unrelated error")
	}
}

// TestWaitForLockReleaseBlocksUntilTheHolderLetsGo is the mechanism behind the
// `daemon restart` fix: a daemon closes its socket before it releases its lock,
// so the replacement has to wait for the lock.
func TestWaitForLockReleaseBlocksUntilTheHolderLetsGo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	const hold = 200 * time.Millisecond
	go func() {
		time.Sleep(hold)
		_ = held.Release()
	}()

	start := time.Now()
	if err := WaitForLockRelease(path, 10*time.Second); err != nil {
		t.Fatalf("WaitForLockRelease: %v", err)
	}
	if waited := time.Since(start); waited < hold {
		t.Errorf("WaitForLockRelease returned after %s, before the holder released at %s", waited, hold)
	}

	// The lock really is free: it can be taken now.
	again, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("the lock WaitForLockRelease called free is still held: %v", err)
	}
	again.Release()
}

// TestWaitForLockReleaseTimesOutWithAClearError: a lock nobody ever releases
// must produce a bounded, named failure rather than a hang.
func TestWaitForLockReleaseTimesOutWithAClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	defer held.Release()

	err = WaitForLockRelease(path, 100*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForLockRelease returned nil while the lock was still held")
	}
	for _, want := range []string{path, "still holds"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestWaitForLockReleaseIgnoresAStaleLockFile: a lock file left by a process
// that is gone must not make a restart wait out the whole timeout.
func TestWaitForLockReleaseIgnoresAStaleLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	// A pid nothing holds, and no lock on the file at all.
	if err := os.WriteFile(path, []byte("2147483646\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WaitForLockRelease(path, time.Second); err != nil {
		t.Fatalf("WaitForLockRelease on a stale lock file: %v", err)
	}
}
