//go:build integration

package daemon_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// doctorEnvironmentChecks are the rows that must come back clean on a machine
// where sonar itself is set up correctly: the daemon, its socket, its database
// and the user's config. The integration checks — MCP clients, the skill, the
// hooks, a project's .sonar.yaml, docker — describe the *user's* machine and
// are warnings on a fresh temp HOME by design, so they are not asserted here.
var doctorEnvironmentChecks = []string{
	"config_parses", "config_dir_writable",
	"daemon_reachable", "daemon_version_matches", "daemon_protocol",
	"socket_permissions", "db_ok",
}

// TestDoctorAgainstARunningDaemon is the step's acceptance demo: `sonar doctor`
// against a daemon this test started reports every environment check ok and
// exits 0.
func TestDoctorAgainstARunningDaemon(t *testing.T) {
	e := newEnv(t)
	e.serve()

	out, err := e.command("doctor", "--json",
		"--only", strings.Join(doctorEnvironmentChecks, ",")).CombinedOutput()
	if err != nil {
		t.Fatalf("sonar doctor exited non-zero: %v\n%s", err, out)
	}

	var report rpc.DaemonDoctorResult
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("sonar doctor --json did not print JSON: %v\n%s", err, out)
	}
	if !report.OK {
		t.Errorf("report is not ok:\n%s", out)
	}
	assertHealthy(t, report, doctorEnvironmentChecks)
	if report.DaemonVersion == "" {
		t.Error("the report did not name the daemon's version")
	}
}

// TestDoctorExitsNonZeroWithNoDaemon is the other half of the exit-code
// contract: a script can branch on it.
func TestDoctorExitsNonZeroWithNoDaemon(t *testing.T) {
	e := newEnv(t)
	out, err := e.command("doctor", "--only", "daemon_reachable").CombinedOutput()
	if err == nil {
		t.Fatalf("sonar doctor exited 0 with no daemon running:\n%s", out)
	}
	if !strings.Contains(string(out), "no daemon is listening") {
		t.Errorf("output does not say what is wrong:\n%s", out)
	}
}

// TestDaemonDoctorRPC is what the desktop app calls: the same rows, without
// shelling out, with the CLI-only ones marked as such.
func TestDaemonDoctorRPC(t *testing.T) {
	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := e.connect(ctx)

	var report rpc.DaemonDoctorResult
	if err := c.Call(ctx, "daemon.doctor", rpc.DaemonDoctorParams{
		Only: append([]string{"cli_on_path"}, doctorEnvironmentChecks...),
	}, &report); err != nil {
		t.Fatalf("daemon.doctor: %v", err)
	}
	if !report.OK {
		t.Errorf("daemon.doctor is not ok: %+v", report.Checks)
	}

	byID := map[string]rpc.DoctorCheck{}
	for _, c := range report.Checks {
		byID[c.ID] = c
	}
	cli, ok := byID["cli_on_path"]
	if !ok {
		t.Fatal("daemon.doctor dropped the cli_on_path row instead of skipping it")
	}
	if cli.Status != "skip" || !strings.Contains(cli.Detail, "CLI-only") {
		t.Errorf("cli_on_path = %+v, want a skip whose detail says it is CLI-only", cli)
	}
	// The daemon answers for itself rather than dialling its own socket.
	if d := byID["daemon_reachable"]; d.Status != "ok" || !strings.Contains(d.Detail, "from the daemon itself") {
		t.Errorf("daemon_reachable = %+v", d)
	}
	assertHealthy(t, report, doctorEnvironmentChecks)

	// Same daemon, because a second one costs a whole process: an unknown id
	// must be refused rather than silently answered with an empty report.
	t.Run("an unknown check is refused", func(t *testing.T) {
		var out rpc.DaemonDoctorResult
		err := c.Call(ctx, "daemon.doctor", rpc.DaemonDoctorParams{Only: []string{"nope"}}, &out)
		if err == nil {
			t.Fatal("an unknown check id was accepted")
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("error = %v, want it to name the unknown check", err)
		}
	})

	// A relative --project would mean the daemon's own working directory,
	// which is never what the caller meant.
	t.Run("a relative project is refused", func(t *testing.T) {
		var out rpc.DaemonDoctorResult
		if err := c.Call(ctx, "daemon.doctor", rpc.DaemonDoctorParams{Project: "."}, &out); err == nil {
			t.Fatal("a relative project path was accepted")
		}
	})
}

// assertHealthy fails the test for any of ids that is not ok. A skip is allowed
// only where the platform genuinely has nothing to look at — Windows has no
// socket file — and never silently: the check has to say so.
func assertHealthy(t *testing.T, report rpc.DaemonDoctorResult, ids []string) {
	t.Helper()
	byID := map[string]rpc.DoctorCheck{}
	for _, c := range report.Checks {
		byID[c.ID] = c
	}
	for _, id := range ids {
		got, ok := byID[id]
		if !ok {
			t.Errorf("%s is missing from the report", id)
			continue
		}
		if got.Status == "ok" {
			continue
		}
		if got.Status == "skip" && (id == "socket_permissions" || id == "daemon_version_matches") {
			continue // no socket file on Windows; no CLI to compare over RPC
		}
		t.Errorf("%s = %s (%s / %s), want ok", id, got.Status, got.Summary, got.Detail)
	}
}
