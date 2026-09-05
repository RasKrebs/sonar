//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the daemon in its own session so it survives the shell
// that started it and never receives the terminal's Ctrl+C.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
