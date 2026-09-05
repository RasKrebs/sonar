//go:build windows

package spawn

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobs maps a child pid to the Job Object it was assigned to. Killing the job
// terminates the child and every descendant it spawned, which is Windows'
// equivalent of signalling a Unix process group.
var jobs sync.Map // pid -> windows.Handle

// configure gives the child its own console process group so Ctrl+C can be
// delivered to it (and only it) with GenerateConsoleCtrlEvent. A detached run
// additionally gets no console at all, so it survives its starter.
func configure(cmd *exec.Cmd, detach bool) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if detach {
		flags |= windows.DETACHED_PROCESS
	}
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: flags}
}

// adopt puts the started child into a fresh Job Object. An attached run's job
// dies with its handle, so a `sonar start` that exits takes the tree with it; a
// detached run's job has no such limit and outlives the daemon that made it.
func adopt(cmd *exec.Cmd, detach bool) error {
	if cmd.Process == nil {
		return nil
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating a job object: %w", err)
	}
	if !detach {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(job)
			return fmt.Errorf("configuring the job object: %w", err)
		}
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("opening the child process: %w", err)
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("assigning the child to the job object: %w", err)
	}
	jobs.Store(cmd.Process.Pid, job)
	return nil
}

// signalGroup forwards an interrupt as a console Ctrl+Break to the child's
// process group; anything else terminates the job.
func signalGroup(p *os.Process, sig os.Signal) error {
	if sig == os.Interrupt {
		if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid)); err == nil {
			return nil
		}
	}
	return killGroup(p)
}

// killGroup terminates the child's Job Object, and with it every descendant.
func killGroup(p *os.Process) error {
	if v, ok := jobs.Load(p.Pid); ok {
		job := v.(windows.Handle)
		if err := windows.TerminateJobObject(job, 1); err != nil {
			return fmt.Errorf("terminating the job object: %w", err)
		}
		return nil
	}
	return p.Kill()
}

// signalExitCode is the fallback for an exit status Windows reports without a
// code; there are no signals to translate.
func signalExitCode(*exec.ExitError) int { return 1 }
