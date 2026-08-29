//go:build !windows

package ports

import (
	"fmt"
	"syscall"
)

// KillPID sends a signal to a process by PID.
func KillPID(pid int, force bool) error {
	// PID 0 is the caller's process group on Unix. Never signal it: a failed
	// scan (PID left at 0) would SIGTERM/SIGKILL sonar itself instead of the
	// intended listener.
	if pid <= 0 {
		return fmt.Errorf("refusing to signal invalid PID %d", pid)
	}

	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}

	if err := syscall.Kill(pid, sig); err != nil {
		return fmt.Errorf("failed to kill PID %d: %w", pid, err)
	}

	return nil
}
