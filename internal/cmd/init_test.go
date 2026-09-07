package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/groups"
)

func TestParseInitServices(t *testing.T) {
	got, err := parseInitServices([]string{"api:8000:/healthz", " web : 5173 ", "db:5432"})
	if err != nil {
		t.Fatalf("parseInitServices: %v", err)
	}
	want := []groups.ServiceAdd{
		{Name: "api", Port: 8000, Health: "/healthz"},
		{Name: "web", Port: 5173},
		{Name: "db", Port: 5432},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d services, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Port != want[i].Port || got[i].Health != want[i].Health {
			t.Errorf("service %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A health path may carry a colon of its own; it is the last field, so it
	// keeps everything that is left.
	odd, err := parseInitServices([]string{"api:8000:http://localhost:8000/health"})
	if err != nil {
		t.Fatalf("parseInitServices: %v", err)
	}
	if odd[0].Health != "http://localhost:8000/health" {
		t.Errorf("health = %q, want the whole rest of the value", odd[0].Health)
	}

	for _, bad := range []string{"", "api", "api:", ":8000", "api:http", "api:0", "api:70000", "api:-1"} {
		if _, err := parseInitServices([]string{bad}); err == nil {
			t.Errorf("--service %q was accepted", bad)
		}
	}
}

// TestInitRefusesForceAndMerge: one replaces the file, the other keeps it.
func TestInitRefusesForceAndMerge(t *testing.T) {
	initForceFlag, initMergeFlag = true, true
	t.Cleanup(func() { initForceFlag, initMergeFlag = false, false })

	err := initRun(initCmd, nil)
	if err == nil {
		t.Fatal("--force with --merge was accepted")
	}
	if !strings.Contains(err.Error(), "--merge") {
		t.Errorf("error = %v, want it to name the flags", err)
	}
}

// TestInitMergeAppendsToAnExistingFile drives the merge path over a file with
// comments in it: the CLI and the daemon share the node-level edit, so what
// `sonar init --merge` writes keeps the author's own lines.
func TestInitMergeAppendsToAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, groups.ConfigName)
	const hand = `# hand written
name: demo
services:
  # the queue was here first
  - name: queue
    port: 6379
`
	if err := os.WriteFile(target, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &groups.Config{Name: "demo", Dir: dir, Path: target, Services: []groups.Service{
		{Name: "api", Port: 8000, Cmd: "uv run api"},
	}}
	if err := initMerge(target, cfg); err != nil {
		t.Fatalf("initMerge: %v", err)
	}

	out, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# hand written", "# the queue was here first", "name: api", "port: 8000"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%q is missing from the merged file:\n%s", want, out)
		}
	}

	// The same merge again clashes rather than duplicating the service.
	if err := initMerge(target, cfg); err == nil {
		t.Error("merging the same service twice was accepted")
	}
}
