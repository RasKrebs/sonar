//go:build !windows

package spawn

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configure puts the child in a process group of its own, which is what makes
// a whole dev-server tree signalable as a unit. A detached run additionally
// gets its own session so it survives the terminal it was started from
// (Setsid already implies a new process group).
func configure(cmd *exec.Cmd, detach bool) {
	attr := &syscall.SysProcAttr{}
	if detach {
		attr.Setsid = true
	} else {
		attr.Setpgid = true
	}
	cmd.SysProcAttr = attr
}

// adopt is a no-op on Unix: the process group is set at fork time.
func adopt(*exec.Cmd, bool) error { return nil }

// signalGroup sends sig to the child's whole process group, falling back to the
// process itself when the group is gone (a child that called setsid, say).
func signalGroup(p *os.Process, sig os.Signal) error {
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		return errors.New("spawn: unsupported signal")
	}
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil {
		pgid = p.Pid
	}
	if err := syscall.Kill(-pgid, sysSig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return p.Signal(sig)
	}
	return nil
}

// killGroup SIGKILLs the child's process group.
func killGroup(p *os.Process) error { return signalGroup(p, syscall.SIGKILL) }

// signalExitCode turns "killed by SIGINT" into the shell's 130, the convention
// `sonar start` propagates so a Ctrl+C looks the same as running the command
// directly.
func signalExitCode(err *exec.ExitError) int {
	if ws, ok := err.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
