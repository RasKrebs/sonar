package ports

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Attribution of a systemd unit to a listening process.
//
// A process inherits the cgroup of whatever started it, so "the process sits
// in unit X's cgroup" is far weaker than "the process belongs to unit X".
// On a GitHub Actions runner every step runs inside the runner agent's own
// unit, so a plain test listener would otherwise be named after
// hosted-compute-agent.service. The rules below attribute a unit only when
// the process really is that unit:
//
//   - its cgroup path must end in a "*.service" leaf (a bare "*.slice", a
//     "session-*.scope" or a "user@*.service" leaf is a session/login
//     container, not a service);
//   - it must be the unit's main pid, or reach that main pid through its
//     ancestors without leaving the unit's cgroup on the way;
//   - the unit must not be the ambient unit of the caller — if sonar itself
//     runs inside that unit, the unit is the environment we are observing
//     from, not an attribution.
//
// The main pid is read from /sys/fs/cgroup/<path>/cgroup.procs (no exec).
// `systemctl show -p MainPID` is used only when that file cannot be read.

const (
	defaultProcRoot   = "/proc"
	defaultCgroupRoot = "/sys/fs/cgroup"
	maxAncestorDepth  = 64
)

// cgroupPathOf extracts the systemd cgroup path from /proc/<pid>/cgroup
// contents. It prefers the unified (v2) line, then the name=systemd v1
// controller, then any other controller with a non-root path.
func cgroupPathOf(content string) string {
	var v2, systemd, other string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" || path == "/" {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			if v2 == "" {
				v2 = path
			}
		case strings.Contains(parts[1], "name=systemd"):
			if systemd == "" {
				systemd = path
			}
		default:
			if other == "" {
				other = path
			}
		}
	}
	switch {
	case v2 != "":
		return v2
	case systemd != "":
		return systemd
	default:
		return other
	}
}

// serviceUnitOf returns the systemd service unit a cgroup path belongs to, or
// "" when the path is not a service's own cgroup. Only a "*.service" leaf
// counts: "*.slice", "session-*.scope" and other "*.scope" leaves are
// containers for user sessions and app launches, and a "user@<uid>.service"
// leaf is the per-user systemd manager, not a service the process provides.
func serviceUnitOf(path string) string {
	leaf := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		leaf = path[idx+1:]
	}
	if leaf == "" || !strings.HasSuffix(leaf, ".service") {
		return ""
	}
	if strings.HasPrefix(leaf, "user@") {
		return ""
	}
	return leaf
}

// mainPIDOf returns the first pid listed in a cgroup.procs file, which for a
// service cgroup is the unit's main pid. Returns 0 when the file holds no pid.
func mainPIDOf(cgroupProcs string) int {
	for _, line := range strings.Split(cgroupProcs, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// unitResolver answers "which systemd unit owns this pid?" against a /proc and
// a /sys/fs/cgroup tree. The roots are fields so tests can point it at
// fixtures. One resolver is built per scan; its caches keep the number of file
// reads proportional to the number of distinct pids and units seen.
type unitResolver struct {
	procRoot   string
	cgroupRoot string
	selfPID    int
	// allowExec permits the `systemctl show -p MainPID` fallback. It stays
	// off on the hot path whenever cgroup.procs can be read directly.
	allowExec bool

	selfUnit  string
	selfKnown bool
	paths     map[int]string
	mains     map[string]int
}

func newUnitResolver() *unitResolver {
	// The systemctl fallback is armed only when /sys/fs/cgroup is missing or
	// unreadable, so a normal scan never spawns a process.
	_, err := os.Stat(defaultCgroupRoot)
	return &unitResolver{
		procRoot:   defaultProcRoot,
		cgroupRoot: defaultCgroupRoot,
		selfPID:    os.Getpid(),
		allowExec:  err != nil,
		paths:      map[int]string{},
		mains:      map[string]int{},
	}
}

// cgroupPath returns the systemd cgroup path of a pid ("" when unknown).
func (r *unitResolver) cgroupPath(pid int) string {
	if path, ok := r.paths[pid]; ok {
		return path
	}
	data, err := os.ReadFile(filepath.Join(r.procRoot, strconv.Itoa(pid), "cgroup"))
	path := ""
	if err == nil {
		path = cgroupPathOf(string(data))
	}
	r.paths[pid] = path
	return path
}

// ppid returns the parent pid of a pid, read from /proc/<pid>/status.
func (r *unitResolver) ppid(pid int) int {
	data, err := os.ReadFile(filepath.Join(r.procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "PPid:")
		if !ok {
			continue
		}
		if ppid, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
			return ppid
		}
		return 0
	}
	return 0
}

// mainPID returns the main pid of the unit occupying a cgroup path, or 0 when
// it cannot be determined.
func (r *unitResolver) mainPID(path, unit string) int {
	if pid, ok := r.mains[path]; ok {
		return pid
	}
	pid := 0
	data, err := os.ReadFile(filepath.Join(r.cgroupRoot, filepath.Clean("/"+path), "cgroup.procs"))
	if err == nil {
		pid = mainPIDOf(string(data))
	} else if r.allowExec {
		pid = systemctlMainPID(unit)
	}
	r.mains[path] = pid
	return pid
}

// ambientUnit returns the unit sonar itself runs inside, if any. Processes are
// not attributed to it: it is the environment the scan happens in (a login
// session, a CI runner agent), and every child started there inherits it.
func (r *unitResolver) ambientUnit() string {
	if !r.selfKnown {
		r.selfUnit = serviceUnitOf(r.cgroupPath(r.selfPID))
		r.selfKnown = true
	}
	return r.selfUnit
}

// unitFor returns the systemd unit to attribute to a pid, or "".
func (r *unitResolver) unitFor(pid int) string {
	if pid <= 0 {
		return ""
	}
	path := r.cgroupPath(pid)
	unit := serviceUnitOf(path)
	if unit == "" {
		return ""
	}
	if unit == r.ambientUnit() {
		return ""
	}
	main := r.mainPID(path, unit)
	if main == 0 {
		// The unit's main pid is unknowable here (no readable
		// /sys/fs/cgroup and no systemctl). The cgroup leaf is still a
		// real service that is not our own ambient unit, so keep the
		// pre-existing behaviour rather than dropping the name.
		return unit
	}
	if pid == main {
		return unit
	}
	// Walk up the ancestors. Staying inside the unit's cgroup the whole way
	// and reaching the main pid means the process was forked by the service.
	// Any ancestor in a different cgroup is a session / scope boundary: the
	// process only inherited the cgroup, it does not belong to the unit.
	for a, depth := r.ppid(pid), 0; a > 1 && depth < maxAncestorDepth; a, depth = r.ppid(a), depth+1 {
		if a == main {
			return unit
		}
		if r.cgroupPath(a) != path {
			return ""
		}
	}
	return ""
}

// systemctlMainPID asks systemd for a unit's main pid. Used only when
// /sys/fs/cgroup is not readable, so it never runs on the normal hot path.
func systemctlMainPID(unit string) int {
	if unit == "" {
		return 0
	}
	out, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", unit).Output()
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
