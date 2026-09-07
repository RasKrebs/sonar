//go:build integration

// Step 5A.4's acceptance path through the real binary: `sonar init --service`
// writes a curated `.sonar.yaml`, `sonar init --merge` appends to it, and
// `sonar groups add|rename|remove` edit it through the daemon — which is the
// point, because it means the desktop app, the MCP server and the CLI all reach
// the same handler and the file's comments survive whoever made the edit.
// Run with `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/groups"
)

func TestServiceEditCycleThroughTheDaemon(t *testing.T) {
	e := newEnv(t)
	project := filepath.Join(e.home, "demo")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(project, groups.ConfigName)

	e.serve()

	run := func(args ...string) string {
		t.Helper()
		cmd := e.command(args...)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sonar %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	fails := func(args ...string) string {
		t.Helper()
		cmd := e.command(args...)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("sonar %s should have failed:\n%s", strings.Join(args, " "), out)
		}
		return string(out)
	}
	load := func() *groups.Config {
		t.Helper()
		cfg, err := groups.Load(configPath)
		if err != nil {
			t.Fatalf("loading the file the daemon wrote: %v", err)
		}
		return cfg
	}
	names := func() []string {
		t.Helper()
		cfg := load()
		out := make([]string, 0, len(cfg.Services))
		for _, s := range cfg.Services {
			out = append(out, s.Name)
		}
		return out
	}

	// A curated file in one command: nothing has to be listening for the user
	// to describe what this project is.
	run("init", "--service", "db:5432", "--service", "api:8000:/healthz")
	if got := names(); !equal(got, []string{"db", "api"}) {
		t.Fatalf("services = %v, want the two that were named", got)
	}
	if load().Services[1].Health != "/healthz" {
		t.Errorf("api health = %q, want /healthz", load().Services[1].Health)
	}

	// A comment the author adds by hand has to survive every edit below.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	withComment := strings.Replace(string(raw), "services:", "# the services, in order\nservices:", 1)
	if err := os.WriteFile(configPath, []byte(withComment), 0o644); err != nil {
		t.Fatal(err)
	}

	// init refuses to overwrite, and says both ways past it.
	refusal := fails("init")
	if !strings.Contains(refusal, "--force") || !strings.Contains(refusal, "--merge") {
		t.Errorf("the refusal should name both ways out:\n%s", refusal)
	}
	// --merge appends instead of refusing; nothing is listening in this
	// checkout, so the proposal is empty and there is nothing to append.
	if out := fails("init", "--merge"); !strings.Contains(out, "nothing to merge") {
		t.Errorf("an empty merge should say so:\n%s", out)
	}

	// add
	run("groups", "add", "demo", "worker", "--port", "9000",
		"--cmd", "uv run worker", "--health", "/live", "--color", "#7c3aed",
		"--description", "background jobs", "--depends-on", "db")
	if got := names(); !equal(got, []string{"db", "api", "worker"}) {
		t.Fatalf("services = %v, want worker appended", got)
	}
	worker := load().Services[2]
	if worker.Port != 9000 || worker.Cmd != "uv run worker" || worker.Health != "/live" ||
		worker.Color != "#7c3aed" || worker.Description != "background jobs" ||
		len(worker.DependsOn) != 1 || worker.DependsOn[0] != "db" {
		t.Errorf("worker = %+v", worker)
	}

	// A name and a port the file already has are both refused.
	if out := fails("groups", "add", "demo", "api", "--port", "9100"); !strings.Contains(out, "already exists") {
		t.Errorf("a duplicate name should be refused by name:\n%s", out)
	}
	if out := fails("groups", "add", "demo", "other", "--port", "8000"); !strings.Contains(out, "8000") {
		t.Errorf("a duplicate port should be refused by port:\n%s", out)
	}

	// rename, depends_on and all
	run("groups", "rename", "demo", "db", "database")
	if got := names(); !equal(got, []string{"database", "api", "worker"}) {
		t.Fatalf("services = %v, want db renamed", got)
	}
	if deps := load().Services[2].DependsOn; len(deps) != 1 || deps[0] != "database" {
		t.Errorf("worker depends_on = %v, want the rename to have followed", deps)
	}
	if out := fails("groups", "rename", "demo", "nope", "other"); !strings.Contains(out, "no service") {
		t.Errorf("renaming an unknown service should say so:\n%s", out)
	}

	// remove, and the dependency on it
	var res rpc.GroupsConfigSetResult
	out := run("groups", "remove", "demo", "database", "--json")
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decoding --json: %v\n%s", err, out)
	}
	if !res.OK || len(res.Affected) != 1 || res.Affected[0] != "database" {
		t.Errorf("result = %+v", res)
	}
	if got := names(); !equal(got, []string{"api", "worker"}) {
		t.Fatalf("services = %v, want database gone", got)
	}
	if deps := load().Services[1].DependsOn; len(deps) != 0 {
		t.Errorf("worker depends_on = %v, want the removed service dropped", deps)
	}

	// The whole cycle went through the daemon, and the hand-written comment is
	// still there.
	final, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(final), "# the services, in order") {
		t.Errorf("the comment was lost somewhere in the cycle:\n%s", final)
	}
	if !strings.Contains(string(final), "written by") {
		t.Errorf("`sonar init`'s own header was lost:\n%s", final)
	}

	// `sonar groups demo` still reads the file the edits left behind.
	if out := run("groups", "demo"); !strings.Contains(out, "worker") {
		t.Errorf("`sonar groups demo` does not show the edited services:\n%s", out)
	}

	e.stopDaemon(0)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
