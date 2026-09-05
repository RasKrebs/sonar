//go:build windows

package killer

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Windows has no signals. `taskkill` without /F posts WM_CLOSE / CTRL_BREAK to
// the process (the SIGTERM equivalent), and /F terminates it outright (the
// SIGKILL equivalent). /T adds the whole process tree, which is what a Job
// Object would give us without needing golang.org/x/sys/windows.
func taskkill(args ...string) error {
	out, err := exec.Command("taskkill", args...).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not found"):
		return codedf(CodeNotFound, "", "%s", msg)
	case strings.Contains(lower, "access is denied"), strings.Contains(lower, "denied"):
		return &CodedError{
			Code:   CodePermissionDenied,
			Detail: msg,
			Hint:   "re-run from an elevated prompt",
			Err:    err,
		}
	}
	return codedf(CodeInternal, "", "%s", msg)
}

func killArgs(pid int, force, tree bool) []string {
	args := []string{"/PID", strconv.Itoa(pid)}
	if tree {
		args = append(args, "/T")
	}
	if force {
		args = append([]string{"/F"}, args...)
	}
	return args
}

// signalProcess terminates a single process.
func signalProcess(pid int, force bool) error {
	if pid <= 0 {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	return taskkill(killArgs(pid, force, false)...)
}

// signalTree terminates a process and everything below it in one call.
func signalTree(pid int, force bool) error {
	if pid <= 0 {
		return codedf(CodeInvalidSelector, "", "refusing to signal invalid PID %d", pid)
	}
	return taskkill(killArgs(pid, force, true)...)
}

// signalGroup is unreachable on Windows: processGroup never reports a group.
func signalGroup(pgid int, force bool) error { return signalTree(pgid, force) }

// hasNativeTreeKill reports that Windows terminates a tree by itself, so the
// engine emits a single row per target instead of walking a PPID table it
// cannot build cheaply.
func hasNativeTreeKill() bool { return true }

// processGroup has no Windows equivalent that is safe without a Job Object
// handle held from spawn time; tree termination goes through taskkill /T.
func processGroup(pid int) (int, bool) { return 0, false }

// pidAlive reports whether the process still exists, via tasklist's filter.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// SetProcessGroup gives a child its own console process group so that a later
// taskkill /T stops the whole tree and a Ctrl+Break aimed at it does not travel
// back to sonar.
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
