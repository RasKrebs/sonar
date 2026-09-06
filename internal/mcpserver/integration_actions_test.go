//go:build integration

// Step 2A.2's acceptance demo in executable form: against a real daemon and a
// real git repo, an agent starts a service through `start_service`, sees it in
// `list_groups`, plans a `kill` with dry_run and then frees the port.
//
// Run with `go test -tags integration -run TestActionToolsDemo -v ./internal/mcpserver/...`.
package mcpserver_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/mcpserver"
)

func TestActionToolsDemo(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	e := newRealEnv(t)
	repo := e.gitRepo(t, "shop")
	port := freePort(t)
	session := e.connect(t)

	// 1. Start a service under sonar and wait for it to accept connections.
	started := callTool(t, session, "start_service", map[string]any{
		"command":         []any{python, "-m", "http.server", strconv.Itoa(port)},
		"cwd":             repo,
		"name":            "docs",
		"wait_for_port":   port,
		"timeout_seconds": 30,
	})
	t.Logf("start_service →\n%s", textOf(started))
	if started.IsError {
		t.Fatalf("start_service failed: %s\nstderr:\n%s", textOf(started), e.stderr())
	}
	var svc mcpserver.StartServiceOutput
	decodeStructured(t, started, &svc)
	t.Cleanup(func() {
		if isListening(port) {
			e.run(t, "kill", "--force", strconv.Itoa(port))
		}
	})

	if svc.RunID == "" || svc.PID == 0 {
		t.Fatalf("start_service returned no run: %+v", svc)
	}
	if len(svc.Ports) != 1 || svc.Ports[0].Port != port {
		t.Fatalf("start_service reported ports %+v, want the row for %d", svc.Ports, port)
	}
	if svc.Ports[0].Group == nil || *svc.Ports[0].Group != "shop" {
		t.Errorf("the port was not attributed to the repo's group: %+v", svc.Ports[0])
	}
	if svc.LogPath == "" {
		t.Error("start_service did not report where the logs went")
	}

	// 2. The group listing shows what was started.
	groups := callTool(t, session, "list_groups", map[string]any{})
	t.Logf("list_groups →\n%s", textOf(groups))
	if groups.IsError {
		t.Fatalf("list_groups failed: %s", textOf(groups))
	}
	var groupList mcpserver.ListGroupsOutput
	decodeStructured(t, groups, &groupList)
	found := false
	for _, g := range groupList.Groups {
		if g.Name == "shop" && containsInt(g.Members, port) {
			found = true
		}
	}
	if !found {
		t.Errorf("group shop does not list port %d: %s", port, mustJSON(t, groupList.Groups))
	}

	// 3. Plan the kill before doing it.
	dry := callTool(t, session, "kill", map[string]any{"port": port, "dry_run": true})
	t.Logf("kill {port: %d, dry_run: true} →\n%s", port, textOf(dry))
	if dry.IsError {
		t.Fatalf("the dry run failed: %s", textOf(dry))
	}
	var plan mcpserver.KillOutput
	decodeStructured(t, dry, &plan)
	if !plan.DryRun || len(plan.Killed) == 0 {
		t.Fatalf("dry run = %+v, want a plan", plan)
	}
	if !isListening(port) {
		t.Fatal("the dry run stopped the service")
	}

	// 4. Free the port for real.
	killed := callTool(t, session, "kill", map[string]any{"port": port})
	t.Logf("kill {port: %d} →\n%s", port, textOf(killed))
	if killed.IsError {
		t.Fatalf("kill failed: %s", textOf(killed))
	}
	var out mcpserver.KillOutput
	decodeStructured(t, killed, &out)
	if len(out.Killed) == 0 || len(out.Failed) != 0 {
		t.Fatalf("kill = %+v", out)
	}

	deadline := time.Now().Add(5 * time.Second)
	for isListening(port) {
		if time.Now().After(deadline) {
			t.Fatalf("port %d is still listening after kill", port)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("port %d is free", port)
}

// TestRenamePortOverStdio names a port this test owns and reads the row back.
func TestRenamePortOverStdio(t *testing.T) {
	e := newRealEnv(t)
	port := e.listen(t)
	session := e.connect(t)

	res := callTool(t, session, "rename_port", map[string]any{"port": port, "name": "itest-listener"})
	t.Logf("rename_port →\n%s", textOf(res))
	if res.IsError {
		t.Fatalf("rename_port failed: %s\nstderr:\n%s", textOf(res), e.stderr())
	}
	var out mcpserver.RenamePortOutput
	decodeStructured(t, res, &out)
	if out.Port.Name == nil || *out.Port.Name != "itest-listener" {
		t.Fatalf("the row does not carry the name: %+v", out.Port)
	}
}

// TestKillNothingIsAFailedRow: an unused port is a result an agent can read,
// not a protocol error.
func TestKillNothingIsAFailedRow(t *testing.T) {
	e := newRealEnv(t)
	session := e.connect(t)

	res := callTool(t, session, "kill", map[string]any{"port": freePort(t)})
	if res.IsError {
		t.Fatalf("kill of an unused port should not be an error result: %s", textOf(res))
	}
	var out mcpserver.KillOutput
	decodeStructured(t, res, &out)
	if len(out.Killed) != 0 || len(out.Failed) != 1 {
		t.Fatalf("kill = %+v, want one failed row", out)
	}
	t.Logf("kill of an unused port →\n%s", textOf(res))
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

// gitRepo makes a repository inside the test's HOME, which is what sonar
// resolves a group from — and what the daemon requires of a spawn's cwd.
func (e *realEnv) gitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(e.home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func isListening(port int) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func containsInt(xs []int, want int) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
