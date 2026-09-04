//go:build !windows

package runs

import "syscall"

// pidAlive reports whether a process with the given PID currently exists.
// Signal 0 performs error checking without actually sending a signal: a nil
// error (or EPERM, meaning the process exists but we can't signal it) means the
// process is alive; ESRCH means it's gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
