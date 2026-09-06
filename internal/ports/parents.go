package ports

import (
	"strconv"
	"strings"
)

// ParentTable returns a pid -> ppid map for every process this user can see.
// It is the same table the scanner builds while enriching listeners, exported
// so the daemon's runs registry can walk a listener's ancestry back to the
// `sonar start` that owns it without scanning ports first.
//
// On Linux the table comes straight from /proc: no exec, no output parsing,
// and — unlike `ps` — nothing to install. procps is absent from plenty of
// container images, and a missing `ps` used to mean every ancestry walk came
// back empty, so a listener a run had spawned was never attributed to it.
// Everywhere else, and if /proc is unreadable, one `ps -A` call still answers.
func ParentTable() map[int]int {
	if table := nativeParentTable(); len(table) > 0 {
		return table
	}
	info := batchGetPPIDsAndCommands()
	out := make(map[int]int, len(info))
	for pid, e := range info {
		out[pid] = e.ppid
	}
	return out
}

// parseProcStatPPID reads the parent pid out of one /proc/<pid>/stat line.
//
// Field 2 is the executable name in parentheses and it may contain spaces and
// parentheses of its own — Next.js sets its process title to
// "next-server (v15.0.1)", so the line reads `42 (next-server (v15.0.1)) S 7
// …`. Splitting on whitespace from the left therefore mis-reads the state and
// the ppid; the scan starts after the *last* ')' instead, which is where the
// kernel's own documentation says the fixed-width fields resume: state, then
// ppid.
func parseProcStatPPID(stat string) (int, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid < 0 {
		return 0, false
	}
	return ppid, true
}
