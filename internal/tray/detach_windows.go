//go:build windows

package tray

import "os/exec"

// detachProcess is a no-op on Windows: a GUI application started from a console
// already runs on its own, and there is no session to leave.
func detachProcess(cmd *exec.Cmd) {}
