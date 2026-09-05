package killer

import (
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Process is one row of the system process table: enough to walk ancestry and
// to name a process in a result row.
type Process struct {
	PID     int
	PPID    int
	Command string
}

// ProcessTable is a pid -> Process snapshot of the machine, taken once per
// KillPorts call so that a tree walk never races a moving process table.
type ProcessTable map[int]Process

// maxTreeDepth bounds every ancestry/descendant walk so a pathological or
// cyclic table (pid 1 reparenting, a pid reported as its own parent) can never
// spin forever.
const maxTreeDepth = 64

// Children returns the direct children of pid, sorted by pid for determinism.
func (t ProcessTable) Children(pid int) []int {
	var out []int
	for child, p := range t {
		if p.PPID == pid && child != pid {
			out = append(out, child)
		}
	}
	sort.Ints(out)
	return out
}

// Descendants returns pid and every process below it, children before parents
// (post-order). Signalling in this order stops a supervisor's workers before
// the supervisor itself, so a restart-on-exit parent has nothing left to
// restart. pid itself is always last.
//
// Processes not present in the table (a pid the scan saw but ps did not, or a
// table this platform cannot build) still yield the single pid, so callers get
// the same shape either way.
func (t ProcessTable) Descendants(pid int) []int {
	if pid <= 0 {
		return nil
	}
	seen := map[int]bool{}
	var walk func(int, int) []int
	walk = func(p, depth int) []int {
		if seen[p] || depth > maxTreeDepth {
			return nil
		}
		seen[p] = true
		var out []int
		for _, c := range t.Children(p) {
			out = append(out, walk(c, depth+1)...)
		}
		return append(out, p)
	}
	return walk(pid, 0)
}

// Ancestors returns the chain above pid, closest parent first, stopping at
// pid 1 (or at the first pid missing from the table).
func (t ProcessTable) Ancestors(pid int) []int {
	var out []int
	seen := map[int]bool{pid: true}
	for cur, depth := pid, 0; depth < maxTreeDepth; depth++ {
		p, ok := t[cur]
		if !ok || p.PPID <= 1 || seen[p.PPID] {
			break
		}
		seen[p.PPID] = true
		out = append(out, p.PPID)
		cur = p.PPID
	}
	return out
}

// Name returns a short, human-usable name for a pid: the basename of the
// command's first word. Empty when the pid is unknown.
func (t ProcessTable) Name(pid int) string {
	p, ok := t[pid]
	if !ok || p.Command == "" {
		return ""
	}
	first := strings.Fields(p.Command)[0]
	if i := strings.LastIndexAny(first, "/\\"); i >= 0 && i+1 < len(first) {
		first = first[i+1:]
	}
	return first
}

// scanProcessTable snapshots the process table. On unix this is the same
// `ps -A` call the scanner's enrichment uses; on Windows there is no cheap
// equivalent and none is needed, because the Windows killer delegates tree
// termination to `taskkill /T`.
func scanProcessTable() ProcessTable {
	if runtime.GOOS == "windows" {
		return ProcessTable{}
	}
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return ProcessTable{}
	}
	return parseProcessTable(string(out))
}

// parseProcessTable parses `ps -A -o pid=,ppid=,command=` output. Split out so
// tests can feed a fixture without running ps.
func parseProcessTable(out string) ProcessTable {
	table := ProcessTable{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
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
		// Reassemble the command, which may itself contain spaces.
		cmdStart := strings.Index(line, fields[1]) + len(fields[1])
		table[pid] = Process{
			PID:     pid,
			PPID:    ppid,
			Command: strings.TrimSpace(line[cmdStart:]),
		}
	}
	return table
}
