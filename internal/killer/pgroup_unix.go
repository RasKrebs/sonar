//go:build !windows

package killer

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// signalOf maps the force flag onto the signal actually sent.
func signalOf(force bool) syscall.Signal {
	if force {
		return syscall.SIGKILL
	}
	return syscall.SIGTERM
}

// signalProcess signals a single pid.
func signalProcess(pid int, force bool) error {
	// PID 0 is the caller's own process group on unix, and a negative pid is a
	// process group. A failed scan must never turn into "signal everything we
	// are part of", so anything that is not a plain positive pid is refused.
	if pid <= 0 {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	if err := syscall.Kill(pid, signalOf(force)); err != nil {
		return mapSignalErr(pid, err)
	}
	return nil
}

// signalGroup signals every member of a process group.
func signalGroup(pgid int, force bool) error {
	if pgid <= 1 {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid process group %d", pgid)
	}
	if err := syscall.Kill(-pgid, signalOf(force)); err != nil {
		return mapSignalErr(pgid, err)
	}
	return nil
}

// signalTree is the Windows entry point for whole-tree termination. On unix the
// engine walks the PPID table itself (children before parents), so this is only
// the single-process fallback.
func signalTree(pid int, force bool) error { return signalProcess(pid, force) }

// hasNativeTreeKill reports whether the platform terminates a process tree by
// itself. Unix does not: the engine orders and signals the tree.
func hasNativeTreeKill() bool { return false }

// processGroup returns the process group led by pid, if pid is a group leader
// and that group is not our own. Signalling a group we belong to would kill
// sonar (and, when sonar was started from a shell job, the shell's whole job),
// so a shared group is reported as unavailable and the caller falls back to
// walking the tree.
func processGroup(pid int) (int, bool) {
	if pid <= 1 {
		return 0, false
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid <= 1 {
		return 0, false
	}
	// Only a leader's group is safe to signal wholesale: it is exactly the
	// process and its descendants that were spawned with Setpgid.
	if pgid != pid {
		return 0, false
	}
	if own, err := syscall.Getpgid(0); err == nil && own == pgid {
		return 0, false
	}
	return pgid, true
}

// pidAlive reports whether the process still exists. Signal 0 checks for
// existence without delivering anything; EPERM means it exists but belongs to
// someone else.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// SetProcessGroup puts a child in its own process group so that the whole tree
// it spawns can later be stopped with one signal. Spawners (`sonar start`, the
// daemon's runs.spawn) call this; the killer then finds the group through
// processGroup and signals it instead of walking the PPID table.
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// mapSignalErr turns a kill(2) errno into the contract's error model.
func mapSignalErr(pid int, err error) error {
	switch {
	case errors.Is(err, syscall.ESRCH):
		return codedf(CodeNotFound, "", "PID %d is already gone", pid)
	case errors.Is(err, syscall.EPERM):
		return permissionErr(pid, err)
	}
	return fmt.Errorf("signalling PID %d: %w", pid, err)
}
