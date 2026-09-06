package ports

import (
	"os"
	"strconv"
)

// nativeParentTable reads pid -> ppid from /proc, the authoritative source on
// Linux. Every numeric directory under /proc is a process; its stat file
// carries the parent. Processes that exit mid-walk are skipped.
func nativeParentTable() map[int]int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make(map[int]int, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		if ppid, ok := parseProcStatPPID(string(stat)); ok {
			out[pid] = ppid
		}
	}
	return out
}
