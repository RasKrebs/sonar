//go:build integration && !windows

package daemon_test

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts a command in a process group of its own so the test can
// signal it separately from the `go test` process it was started from.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree SIGKILLs a command, everything below it in the process table and
// its process group. All three: a `sonar start` child gets a process group of
// its own, so the group kill alone would miss it, and the process table can be
// a scan behind.
func killTree(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	for _, p := range descendants(pid) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
