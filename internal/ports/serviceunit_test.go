package ports

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestCgroupPathOf(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "cgroup v2",
			in:   "0::/system.slice/nginx.service\n",
			want: "/system.slice/nginx.service",
		},
		{
			name: "cgroup v1 name=systemd wins over other controllers",
			in: "12:freezer:/\n" +
				"11:devices:/system.slice\n" +
				"1:name=systemd:/system.slice/postgresql.service\n" +
				"0::/\n",
			want: "/system.slice/postgresql.service",
		},
		{
			name: "cgroup v1 without name=systemd falls back to any controller",
			in:   "12:freezer:/\n11:devices:/system.slice/postgresql.service\n0::/\n",
			want: "/system.slice/postgresql.service",
		},
		{
			name: "root only",
			in:   "0::/\n",
			want: "",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "malformed lines ignored",
			in:   "garbage\n0::/system.slice/ssh.service\n",
			want: "/system.slice/ssh.service",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cgroupPathOf(tc.in); got != tc.want {
				t.Errorf("cgroupPathOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServiceUnitOf(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"system service", "/system.slice/nginx.service", "nginx.service"},
		{"user service", "/user.slice/user-1000.slice/user@1000.service/app.slice/myapp.service", "myapp.service"},
		{"login session scope", "/user.slice/user-1000.slice/session-3.scope", ""},
		{"app scope", "/user.slice/user-1000.slice/user@1000.service/app.slice/app-terminal.scope", ""},
		{"slice only", "/user.slice/user-1000.slice", ""},
		{"user manager itself", "/user.slice/user-1000.slice/user@1000.service", ""},
		{"machine scope", "/machine.slice/docker-abc.scope", ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := serviceUnitOf(tc.path); got != tc.want {
				t.Errorf("serviceUnitOf(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestMainPIDOf(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"single pid", "1234\n", 1234},
		{"first of many", "1234\n1235\n1240\n", 1234},
		{"blank lines skipped", "\n\n99\n", 99},
		{"empty", "", 0},
		{"garbage", "not-a-pid\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mainPIDOf(tc.in); got != tc.want {
				t.Errorf("mainPIDOf(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// fakeProc is one process in the fake /proc tree: the cgroup line captured
// from a real system and the parent that started it.
type fakeProc struct {
	pid    int
	ppid   int
	cgroup string
}

// writeFakeSystem materialises a /proc tree and a /sys/fs/cgroup tree in temp
// dirs. cgroupProcs maps a cgroup path to the pids listed in its cgroup.procs
// (first one is the unit's main pid); a path left out of the map has no
// cgroup.procs file, standing in for an unreadable /sys/fs/cgroup.
func writeFakeSystem(t *testing.T, procs []fakeProc, cgroupProcs map[string][]int) (procRoot, cgroupRoot string) {
	t.Helper()
	root := t.TempDir()
	procRoot = filepath.Join(root, "proc")
	cgroupRoot = filepath.Join(root, "cgroup")
	for _, p := range procs {
		dir := filepath.Join(procRoot, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "cgroup"), "0::"+p.cgroup+"\n")
		write(t, filepath.Join(dir, "status"),
			"Name:\tproc"+strconv.Itoa(p.pid)+"\nPPid:\t"+strconv.Itoa(p.ppid)+"\nUid:\t0\n")
	}
	for path, pids := range cgroupProcs {
		dir := filepath.Join(cgroupRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := ""
		for _, pid := range pids {
			content += strconv.Itoa(pid) + "\n"
		}
		write(t, filepath.Join(dir, "cgroup.procs"), content)
	}
	return procRoot, cgroupRoot
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	sshCgroup      = "/system.slice/ssh.service"
	pgCgroup       = "/system.slice/postgresql.service"
	agentCgroup    = "/system.slice/hosted-compute-agent.service"
	sessionCgroup  = "/user.slice/user-1000.slice/session-3.scope"
	appScopeCgroup = "/user.slice/user-1000.slice/user@1000.service/app.slice/app-terminal.scope"
	userSvcCgroup  = "/user.slice/user-1000.slice/user@1000.service/app.slice/myapp.service"
	orphanCgroup   = "/system.slice/orphan.service"
	cronCgroup     = "/system.slice/cron.service"
)

// fakeSystem models one machine: a couple of genuine services, a GitHub
// runner agent that spawned a test listener, a login session with a dev
// server, and a per-user systemd service.
func fakeSystem() ([]fakeProc, map[string][]int) {
	procs := []fakeProc{
		{pid: 1, ppid: 0, cgroup: "/init.scope"},

		// Genuine system services started by systemd.
		{pid: 100, ppid: 1, cgroup: sshCgroup},   // sshd main pid
		{pid: 101, ppid: 100, cgroup: sshCgroup}, // per-connection sshd
		{pid: 200, ppid: 1, cgroup: pgCgroup},    // postgres main pid
		{pid: 201, ppid: 200, cgroup: pgCgroup},  // postgres worker

		// GitHub runner: the agent unit, its worker, and the test binary
		// the workflow started. All three sit in the agent's cgroup.
		{pid: 300, ppid: 1, cgroup: agentCgroup},
		{pid: 301, ppid: 300, cgroup: agentCgroup},
		{pid: 302, ppid: 301, cgroup: agentCgroup}, // the test listener
		{pid: 999, ppid: 301, cgroup: agentCgroup}, // sonar itself

		// Interactive login session and a dev server started from it.
		{pid: 400, ppid: 1, cgroup: sessionCgroup},
		{pid: 401, ppid: 400, cgroup: sessionCgroup},
		{pid: 500, ppid: 1, cgroup: appScopeCgroup},

		// A per-user systemd service and a child it forked.
		{pid: 501, ppid: 1, cgroup: userSvcCgroup},
		{pid: 502, ppid: 501, cgroup: userSvcCgroup},

		// In cron.service's cgroup but started from the login session:
		// inherited the cgroup, does not descend from the unit's main pid.
		{pid: 600, ppid: 1, cgroup: cronCgroup},
		{pid: 601, ppid: 400, cgroup: cronCgroup},

		// A service whose cgroup.procs is not readable.
		{pid: 700, ppid: 1, cgroup: orphanCgroup},
	}
	cgroupProcs := map[string][]int{
		sshCgroup:      {100, 101},
		pgCgroup:       {200, 201},
		agentCgroup:    {300, 301, 302, 999},
		sessionCgroup:  {400, 401},
		appScopeCgroup: {500},
		userSvcCgroup:  {501, 502},
		cronCgroup:     {600, 601},
	}
	return procs, cgroupProcs
}

func TestUnitResolverAttribution(t *testing.T) {
	procs, cgroupProcs := fakeSystem()
	procRoot, cgroupRoot := writeFakeSystem(t, procs, cgroupProcs)

	tests := []struct {
		name    string
		selfPID int // the pid sonar runs as
		pid     int
		want    string
	}{
		// Genuine services keep their unit, whether the listener is the
		// main pid or a process the service forked.
		{"sshd main pid", 999, 100, "ssh.service"},
		{"sshd session child", 999, 101, "ssh.service"},
		{"postgres main pid", 999, 200, "postgresql.service"},
		{"postgres worker", 999, 201, "postgresql.service"},

		// The reported bug: on a runner every step inherits the agent's
		// cgroup, and sonar runs inside it too, so the unit is ambient.
		{"runner agent main pid is ambient", 999, 300, ""},
		{"runner worker is ambient", 999, 301, ""},
		{"test listener on a runner", 999, 302, ""},

		// Login sessions and app scopes are not services at all.
		{"login shell", 999, 400, ""},
		{"dev server started from a shell", 999, 401, ""},
		{"process in an app scope", 999, 500, ""},

		// systemd --user services are real services.
		{"user service main pid", 999, 501, "myapp.service"},
		{"user service child", 999, 502, "myapp.service"},

		// Same cgroup, but the ancestor chain leaves the unit before
		// reaching its main pid.
		{"cron main pid", 999, 600, "cron.service"},
		{"session process inheriting a service cgroup", 999, 601, ""},

		// No readable cgroup.procs: keep the unit rather than losing the
		// name of a genuine service.
		{"service with unknown main pid", 999, 700, "orphan.service"},

		// Scanning from a login session instead of inside the agent unit:
		// genuine services are unaffected, sessions still get nothing.
		{"sshd seen from a login shell", 400, 100, "ssh.service"},
		{"dev server seen from a login shell", 400, 401, ""},
		{"app scope seen from a login shell", 400, 500, ""},

		{"unknown pid", 999, 12345, ""},
		{"invalid pid", 999, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &unitResolver{
				procRoot:   procRoot,
				cgroupRoot: cgroupRoot,
				selfPID:    tc.selfPID,
				paths:      map[int]string{},
				mains:      map[string]int{},
			}
			if got := r.unitFor(tc.pid); got != tc.want {
				t.Errorf("unitFor(%d) = %q, want %q", tc.pid, got, tc.want)
			}
		})
	}
}

// The main-pid lookup must come from /sys/fs/cgroup, never from spawning
// systemctl, whenever the cgroup tree is readable.
func TestUnitResolverReadsMainPIDFromCgroupTree(t *testing.T) {
	procs, cgroupProcs := fakeSystem()
	// Re-exec'd service: the main pid is not the lowest pid in the cgroup.
	cgroupProcs[pgCgroup] = []int{201, 200}
	procRoot, cgroupRoot := writeFakeSystem(t, procs, cgroupProcs)

	r := &unitResolver{
		procRoot:   procRoot,
		cgroupRoot: cgroupRoot,
		selfPID:    999,
		paths:      map[int]string{},
		mains:      map[string]int{},
	}
	if got := r.mainPID(pgCgroup, "postgresql.service"); got != 201 {
		t.Fatalf("mainPID = %d, want 201 (first pid in cgroup.procs)", got)
	}
	// 201 is now the main pid, so it is attributed; 200 is its parent, not
	// its descendant, so it is not.
	if got := r.unitFor(201); got != "postgresql.service" {
		t.Errorf("unitFor(201) = %q, want postgresql.service", got)
	}
	if got := r.unitFor(200); got != "" {
		t.Errorf("unitFor(200) = %q, want empty", got)
	}
}

// A cycle or a very deep ancestor chain must not hang the walk.
func TestUnitResolverAncestorCycle(t *testing.T) {
	procs := []fakeProc{
		{pid: 10, ppid: 11, cgroup: sshCgroup},
		{pid: 11, ppid: 10, cgroup: sshCgroup},
	}
	procRoot, cgroupRoot := writeFakeSystem(t, procs, map[string][]int{sshCgroup: {9, 10, 11}})
	r := &unitResolver{
		procRoot:   procRoot,
		cgroupRoot: cgroupRoot,
		selfPID:    999, // not in the tree: nothing is ambient here

		paths: map[int]string{},
		mains: map[string]int{},
	}
	if got := r.unitFor(11); got != "" {
		t.Errorf("unitFor(11) = %q, want empty", got)
	}
}
