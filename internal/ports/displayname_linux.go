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

// batchGetServiceUnits returns pid -> systemd unit for the pids that really
// belong to a unit. Membership in a unit's cgroup is not enough: a process
// inherits the cgroup of whoever started it, so a shell (or a CI runner's test
// binary) would otherwise be named after the ambient session or agent unit.
// See unitResolver for the attribution rules.
func batchGetServiceUnits(pids []int) map[int]string {
	result := make(map[int]string)
	r := newUnitResolver()
	for _, pid := range pids {
		if unit := r.unitFor(pid); unit != "" {
			result[pid] = unit
		}
	}
	return result
}
