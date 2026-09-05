package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/killer"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

func sampleResults() []killer.Result {
	return []killer.Result{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 400, Name: "esbuild", Method: state.MethodSIGTERM, OK: true},
		{Port: 3000, BindAddress: "127.0.0.1", PID: 300, Name: "vite", Method: state.MethodSIGKILL, OK: true},
		{Port: 5432, BindAddress: "0.0.0.0", PID: 0, Name: "db", Method: state.MethodDockerStop, OK: true},
		{Port: 8080, BindAddress: "127.0.0.1", PID: 900, Name: "nginx", Method: state.MethodNone, OK: false,
			Error: "not permitted to signal PID 900"},
	}
}

// The JSON shape is the cross-spec contract's kill result list; other repos are
// built against it, so it is pinned here character for character.
func TestKillJSONGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := writeKillJSON(&buf, sampleResults()); err != nil {
		t.Fatal(err)
	}
	const want = `[
  {
    "port": 3000,
    "bind_address": "127.0.0.1",
    "pid": 400,
    "name": "esbuild",
    "method": "sigterm",
    "ok": true
  },
  {
    "port": 3000,
    "bind_address": "127.0.0.1",
    "pid": 300,
    "name": "vite",
    "method": "sigkill",
    "ok": true
  },
  {
    "port": 5432,
    "bind_address": "0.0.0.0",
    "pid": 0,
    "name": "db",
    "method": "docker_stop",
    "ok": true
  },
  {
    "port": 8080,
    "bind_address": "127.0.0.1",
    "pid": 900,
    "name": "nginx",
    "method": "none",
    "ok": false,
    "error": "not permitted to signal PID 900"
  }
]
`
	if got := buf.String(); got != want {
		t.Fatalf("--json output:\n%s\nwant:\n%s", got, want)
	}
}

func TestKillJSONEmptyIsAnEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := writeKillJSON(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "[]\n" {
		t.Fatalf("got %q, want an empty array", got)
	}
}

func TestReportKillJSONExitsNonZeroOnFailure(t *testing.T) {
	var buf bytes.Buffer
	err := reportKill(&buf, sampleResults(), nil, true, false)
	if !errors.Is(err, errSilent) {
		t.Fatalf("err = %v, want the silent sentinel so --json output stands alone", err)
	}
	if !strings.HasPrefix(buf.String(), "[") {
		t.Fatal("the full result list must still be printed")
	}

	ok := sampleResults()[:3]
	if err := reportKill(&bytes.Buffer{}, ok, nil, true, false); err != nil {
		t.Fatalf("err = %v, want nil when every target succeeded", err)
	}
}

func TestReportKillText(t *testing.T) {
	display.NoColor = true
	snapshot := []ports.ListeningPort{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 300},
		{Port: 5432, BindAddress: "0.0.0.0", PID: 900},
	}
	var buf bytes.Buffer
	err := reportKill(&buf, sampleResults(), snapshot, false, false)
	if err == nil {
		t.Fatal("a failed target must make the command exit 1")
	}
	got := buf.String()
	for _, want := range []string{
		"sigterm esbuild (PID 400) on port 3000",
		"sigkill vite (PID 300) on port 3000",
		"docker_stop db on port 5432 (container)",
		"error: not permitted to signal PID 900",
		"Freed http://127.0.0.1:3000",
		"Freed http://localhost:5432",
		"3/4 stopped.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

func TestReportKillDryRunSaysWould(t *testing.T) {
	display.NoColor = true
	var buf bytes.Buffer
	if err := reportKill(&buf, sampleResults()[:1], nil, false, true); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "Dry run: 1 action(s), children first.") {
		t.Errorf("missing the dry-run header:\n%s", got)
	}
	if !strings.Contains(got, "would sigterm esbuild") {
		t.Errorf("a dry run reports what would happen:\n%s", got)
	}
	if strings.Contains(got, "stopped.") || strings.Contains(got, "Freed") {
		t.Errorf("a dry run must not claim anything happened:\n%s", got)
	}
}

// ------------------------------------------------------------- selectors ---

func resetKillFlags(t *testing.T) {
	t.Helper()
	killPIDFlag, killGroupFlag, killAllFlag = nil, "", false
	killFilterFlag, killProjectFlag = "", ""
	t.Cleanup(func() {
		killPIDFlag, killGroupFlag, killAllFlag = nil, "", false
		killFilterFlag, killProjectFlag = "", ""
	})
}

func killSnapshot() []ports.ListeningPort {
	return []ports.ListeningPort{
		{Port: 3000, PID: 300, BindAddress: "127.0.0.1", Group: "storefront", GroupSource: "auto"},
		{Port: 3001, PID: 301, BindAddress: "127.0.0.1", Tag: "web", RunID: "abc123"},
		{Port: 5432, PID: 900, BindAddress: "0.0.0.0", Type: ports.PortTypeDocker,
			DockerContainer: "pg", DockerComposeProject: "my-app"},
		{Port: 7000, PID: 700, BindAddress: "127.0.0.1", IsApp: true},
	}
}

func TestKillTargetsByPortAndPID(t *testing.T) {
	resetKillFlags(t)
	killPIDFlag = []int{12345}

	targets, confirm, err := killTargets([]string{"3000"}, killSnapshot(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if confirm {
		t.Error("an explicit port or pid needs no confirmation")
	}
	want := []killer.Target{{PID: 12345}, {Port: 3000, BindAddress: "127.0.0.1"}}
	if len(targets) != 2 || targets[0] != want[0] || targets[1] != want[1] {
		t.Fatalf("targets = %+v, want %+v", targets, want)
	}
}

func TestKillTargetsByGroup(t *testing.T) {
	snapshot := killSnapshot()
	cases := map[string][]int{
		"storefront": {3000}, // the scanner's group field
		"web":        {3001}, // a sonar run tag
		"abc123":     {3001}, // or its run id
		"my-app":     {5432}, // a Docker Compose project
		"MY-APP":     {5432}, // case-insensitively
	}
	for name, wantPorts := range cases {
		resetKillFlags(t)
		killGroupFlag = name
		targets, confirm, err := killTargets(nil, snapshot, "")
		if err != nil {
			t.Fatalf("group %q: %v", name, err)
		}
		if !confirm {
			t.Errorf("group %q: a group kill must confirm unless --yes", name)
		}
		if len(targets) != len(wantPorts) || targets[0].Port != wantPorts[0] {
			t.Errorf("group %q: targets = %+v, want ports %v", name, targets, wantPorts)
		}
	}
}

func TestKillTargetsUnknownGroup(t *testing.T) {
	resetKillFlags(t)
	killGroupFlag = "nope"
	if _, _, err := killTargets(nil, killSnapshot(), ""); err == nil {
		t.Fatal("an unknown group must be an error, not an empty sweep")
	}
}

func TestKillTargetsAllSkipsDesktopApps(t *testing.T) {
	resetKillFlags(t)
	killAllFlag = true
	targets, confirm, err := killTargets(nil, killSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !confirm {
		t.Error("--all must confirm unless --yes")
	}
	for _, tg := range targets {
		if tg.Port == 7000 {
			t.Error("--all swept up a desktop app")
		}
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %+v, want the three non-app ports", targets)
	}
}

func TestKillTargetsFilterAndProjectImplyAll(t *testing.T) {
	resetKillFlags(t)
	killProjectFlag = "my-app"
	targets, _, err := killTargets(nil, killSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Port != 5432 {
		t.Fatalf("targets = %+v, want just the compose project's port", targets)
	}

	resetKillFlags(t)
	killFilterFlag = "docker"
	targets, _, err = killTargets(nil, killSnapshot(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Port != 5432 {
		t.Fatalf("targets = %+v, want just the container", targets)
	}

	resetKillFlags(t)
	killFilterFlag = "nonsense"
	if _, _, err := killTargets(nil, killSnapshot(), ""); err == nil {
		t.Fatal("an unknown --filter must be rejected")
	}
}

func TestKillTargetsRejectsMixedSelectors(t *testing.T) {
	resetKillFlags(t)
	killGroupFlag = "storefront"
	if _, _, err := killTargets([]string{"3000"}, killSnapshot(), ""); err == nil {
		t.Error("--group with a positional port must be rejected")
	}

	resetKillFlags(t)
	killAllFlag = true
	if _, _, err := killTargets([]string{"3000"}, killSnapshot(), ""); err == nil {
		t.Error("--all with a positional port must be rejected")
	}

	resetKillFlags(t)
	if _, _, err := killTargets(nil, killSnapshot(), ""); err == nil {
		t.Error("no selector at all must be rejected")
	}

	resetKillFlags(t)
	if _, _, err := killTargets([]string{"http"}, killSnapshot(), ""); err == nil {
		t.Error("a non-numeric argument must be rejected")
	}
}

func TestPositionalTargetReadsAPortFirst(t *testing.T) {
	snapshot := killSnapshot()
	if got := positionalTarget(3000, snapshot, ""); got.Port != 3000 || got.PID != 0 {
		t.Errorf("3000 = %+v, want the listening port", got)
	}
	// Nothing listens on this number and no such process exists: still a port,
	// so the user gets "no process is listening on 4321" rather than a pid error.
	if got := positionalTarget(4321, snapshot, ""); got.Port != 4321 {
		t.Errorf("4321 = %+v, want a port target", got)
	}
	// Out of port range: it can only be a pid.
	if got := positionalTarget(70000, snapshot, ""); got.PID != 70000 || got.Port != 0 {
		t.Errorf("70000 = %+v, want a pid target", got)
	}
}

// A `.sonar.yaml` group only exists after groups.Attribute has run over the
// scan, so `sonar kill -g` misses it unless the kill path enriches the same way
// `sonar list` does. This pins the composition the scan path relies on.
func TestKillTargetsSeeAFileGroup(t *testing.T) {
	// The index resolves symlinks; on macOS t.TempDir() is one, so compare
	// like for like.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: checkout\nports:\n  - 4000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot := []ports.ListeningPort{
		{Port: 4000, PID: 400, BindAddress: "127.0.0.1", Process: "node", Cwd: dir},
	}
	if got := snapshot[0].Group; got != "" {
		t.Fatalf("group = %q before attribution, want it empty", got)
	}
	groups.Attribute(snapshot)
	if got := snapshot[0].Group; got != "checkout" {
		t.Fatalf("group = %q after attribution, want checkout", got)
	}

	resetKillFlags(t)
	killGroupFlag = "checkout"
	targets, _, err := killTargets(nil, snapshot, "")
	if err != nil {
		t.Fatalf("kill -g checkout: %v", err)
	}
	want := killer.Target{Port: 4000, BindAddress: "127.0.0.1"}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets = %+v, want %+v", targets, want)
	}
}
