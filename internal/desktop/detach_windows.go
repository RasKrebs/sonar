//go:build windows

package desktop

import "os/exec"

// detachProcess is a no-op on Windows, which has no installer here yet and no
// session to leave.
func detachProcess(cmd *exec.Cmd) {}
