package ports

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// enrichDisplayNameSignals batches the I/O needed by resolveProcessName:
// parent cmdlines, working directories, and OS service-manager labels.
// All lookups are batched into a small number of subprocess calls so that
// the overhead is bounded even with many listening ports.
func enrichDisplayNameSignals(pp []ListeningPort) {
	if len(pp) == 0 {
		return
	}
	pids := collectPIDs(pp)
	if len(pids) == 0 {
		return
	}

	// 1. PPID + parent cmdline lookup. One ps -A call (cheap, ~few ms)
	//    gives us pid -> ppid -> command for every process on the system,
	//    so we can chase parents without a second exec.
	pidInfo := batchGetPPIDsAndCommands()

	// 2. Cwds. Per-platform: lsof on macOS (one call), readlink /proc on Linux.
	cwds := batchGetCwds(pids)

	// 3. Service manager labels. systemd unit on Linux (cgroup file read,
	//    no exec), launchd label on macOS (one launchctl list call).
	units := batchGetServiceUnits(pids)

	for i := range pp {
		pid := pp[i].PID
		if pid <= 0 {
			continue
		}
		if info, ok := pidInfo[pid]; ok && info.ppid > 1 {
			if parent, ok := pidInfo[info.ppid]; ok {
				pp[i].ParentCmd = parent.cmd
			}
		}
		if cwd, ok := cwds[pid]; ok {
			pp[i].Cwd = cwd
		}
		if unit, ok := units[pid]; ok {
			pp[i].ServiceUnit = unit
		}
	}
}

type pidEntry struct {
	ppid int
	cmd  string
}

// batchGetPPIDsAndCommands returns a pid -> {ppid, command} map for every
// process the current user can see, via a single ps call.
func batchGetPPIDsAndCommands() map[int]pidEntry {
	result := make(map[int]pidEntry)
	if runtime.GOOS == "windows" {
		return result // not supported via this path on Windows
	}
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return result
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// pid ppid command...
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
		// Reassemble the command (it may contain spaces).
		cmdStart := strings.Index(line, fields[1]) + len(fields[1])
		cmd := strings.TrimSpace(line[cmdStart:])
		result[pid] = pidEntry{ppid: ppid, cmd: cmd}
	}
	return result
}
