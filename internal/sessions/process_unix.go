//go:build !windows

package sessions

import (
	"os/exec"
	"strconv"
	"strings"
)

// processTable lists pid, ppid and command in one `ps` call. Detection runs
// once per `sonar start`, so one exec is the whole cost; a failure just means
// the ancestor walk falls back to the parent pid.
func processTable() []Process {
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil
	}
	return parsePS(string(out))
}

// parsePS reads `pid ppid comm` rows. comm may contain spaces (a path), so the
// first two fields are cut off and the rest is the command.
func parsePS(out string) []Process {
	var procs []Process
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		procs = append(procs, Process{PID: pid, PPID: ppid, Command: strings.Join(fields[2:], " ")})
	}
	return procs
}
