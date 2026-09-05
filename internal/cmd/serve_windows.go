//go:build windows

package cmd

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// detachProcess gives the daemon its own process group and no console, so it
// survives the shell that started it and never receives its Ctrl+C.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
