package ports

import (
	"strconv"
	"strings"
	"sync/atomic"
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
//
// Windows has neither: no /proc, and no `ps -A` for batchGetPPIDsAndCommands
// to call, so both of those come back empty and the table used to be empty with
// them — every ancestry walk on Windows failed, and a listener a `sonar start`
// had spawned was never attributed to its run. The fallback is the parents the
// last scan already learned: Get-CimInstance returns ParentProcessId alongside
// the command line it was being asked for anyway, so this costs no process and
// no second query. It covers the pids that have been scanned, which is exactly
// the population the walk starts from.
func ParentTable() map[int]int {
	if table := nativeParentTable(); len(table) > 0 {
		return table
	}
	info := batchGetPPIDsAndCommands()
	if len(info) > 0 {
		out := make(map[int]int, len(info))
		for pid, e := range info {
			out[pid] = e.ppid
		}
		return out
	}
	return scanParents()
}

// scanParentTable holds the pid -> ppid pairs the most recent scan learned, for
// platforms with no process table of their own to read. It is replaced whole,
// never mutated, so a reader always sees one consistent scan's worth.
var scanParentTable atomic.Pointer[map[int]int]

// rememberScanParents records the parents a scan reported.
func rememberScanParents(info map[int]procInfo) {
	table := make(map[int]int, len(info))
	for pid, e := range info {
		if pid > 0 && e.ppid > 0 {
			table[pid] = e.ppid
		}
	}
	if len(table) == 0 {
		return
	}
	scanParentTable.Store(&table)
}

// scanParents returns a copy of what the last scan learned.
func scanParents() map[int]int {
	table := scanParentTable.Load()
	if table == nil {
		return map[int]int{}
	}
	out := make(map[int]int, len(*table))
	for pid, ppid := range *table {
		out[pid] = ppid
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
