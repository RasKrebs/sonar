//go:build darwin

package ports

import (
	"os/exec"
	"strconv"
	"strings"
)

// batchGetCwds returns pid -> cwd for the given PIDs using a single lsof call.
// lsof -a -p PID,PID,... -d cwd -Fpn produces records like:
//
//	p1234
//	n/Users/me/project
//	p5678
//	n/var/empty
func batchGetCwds(pids []int) map[int]string {
	result := make(map[int]string)
	if len(pids) == 0 {
		return result
	}
	pidStrs := make([]string, len(pids))
	for i, p := range pids {
		pidStrs[i] = strconv.Itoa(p)
	}
	out, err := exec.Command("lsof", "-a", "-p", strings.Join(pidStrs, ","), "-d", "cwd", "-Fpn").Output()
	if err != nil {
		return result
	}
	var current int
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(line[1:])
			if err == nil {
				current = pid
			}
		case 'n':
			if current != 0 {
				result[current] = line[1:]
			}
		}
	}
	return result
}

// batchGetServiceUnits returns pid -> launchd label using one launchctl list call.
// Output format: PID Status Label
func batchGetServiceUnits(pids []int) map[int]string {
	result := make(map[int]string)
	if len(pids) == 0 {
		return result
	}
	wanted := make(map[int]bool, len(pids))
	for _, p := range pids {
		wanted[p] = true
	}
	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return result
	}
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || line == "" {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if !wanted[pid] {
			continue
		}
		label := fields[2]
		// Skip anonymous / generic labels — we only want named services.
		if strings.HasPrefix(label, "0x") || label == "-" {
			continue
		}
		// Skip GUI app labels: launchd labels for sandboxed apps look like
		// "application.com.spotify.client.5607150.5607671" — verbose, unstable,
		// and never as good as the .app bundle name we get from the cmdline.
		if strings.HasPrefix(label, "application.") {
			continue
		}
		result[pid] = label
	}
	return result
}
