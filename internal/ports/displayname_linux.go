//go:build linux

package ports

import (
	"os"
	"strconv"
)

// batchGetCwds returns pid -> cwd by reading /proc/<pid>/cwd symlinks.
// No exec needed; this is essentially free.
func batchGetCwds(pids []int) map[int]string {
	result := make(map[int]string)
	for _, pid := range pids {
		if cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
			result[pid] = cwd
		}
	}
	return result
}

// batchGetServiceUnits returns pid -> systemd unit by reading
// /proc/<pid>/cgroup and parsing the systemd cgroup line.
//
// A typical line looks like:
//
//	0::/system.slice/nginx.service
//	0::/user.slice/user-1000.slice/user@1000.service/app.slice/myapp.service
//
// We extract the last "*.service" / "*.scope" segment as the unit name.
func batchGetServiceUnits(pids []int) map[int]string {
	result := make(map[int]string)
	for _, pid := range pids {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
		if err != nil {
			continue
		}
		if unit := parseSystemdUnit(string(data)); unit != "" {
			result[pid] = unit
		}
	}
	return result
}

