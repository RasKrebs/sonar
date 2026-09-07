//go:build !windows

package desktop

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own session, so the app outlives the
// shell that installed it.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
