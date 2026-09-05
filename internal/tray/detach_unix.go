//go:build !windows

package tray

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own session so it outlives the shell
// that ran `sonar tray`.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
