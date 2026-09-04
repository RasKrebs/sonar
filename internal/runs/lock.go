package runs

import (
	"fmt"
	"os"
	"time"
)

// withLock runs fn while holding an exclusive sidecar lock on the registry.
// The lock is an O_CREATE|O_EXCL file: whoever creates it wins. We spin with a
// short backoff and treat a sufficiently old lock file as stale (a previous
// holder that crashed before unlocking) so the registry can never deadlock.
//
// This is intentionally lightweight: contention is rare (only concurrent
// `sonar run` start/stop), holds are sub-millisecond (read+write a tiny file),
// and the cost of an occasional double-write is just a redundant atomic save.
func withLock(fn func() error) error {
	lp := lockPath()
	if err := os.MkdirAll(parentDir(lp), 0o755); err != nil {
		return fmt.Errorf("runs: could not create config dir: %w", err)
	}

	const (
		maxWait     = 2 * time.Second
		staleAfter  = 5 * time.Second
		pollBackoff = 5 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)

	for {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			defer os.Remove(lp)
			return fn()
		}
		if !os.IsExist(err) {
			return fmt.Errorf("runs: acquire lock: %w", err)
		}
		// Lock held by someone else. Break a stale lock left by a crash.
		if info, statErr := os.Stat(lp); statErr == nil {
			if time.Since(info.ModTime()) > staleAfter {
				os.Remove(lp)
				continue
			}
		}
		if time.Now().After(deadline) {
			// Give up waiting and force the lock so we never block forever.
			os.Remove(lp)
			continue
		}
		time.Sleep(pollBackoff)
	}
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
