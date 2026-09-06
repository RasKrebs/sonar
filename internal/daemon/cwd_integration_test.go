//go:build integration

// Roadmap step 1A.10's acceptance path: a listener started inside a git
// checkout is grouped under the repository, on every platform, with no
// `.sonar.yaml` anywhere near it.
//
// Nothing here is guarded by OS, and that is the whole point. Until the PEB
// reader landed, `batchGetCwds` was a no-op on Windows, so every row there had
// an empty cwd, no project_root and no git-root group — and no test said so,
// because the ones that could have were written to skip. Run with
// `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/state"
)

// TestCwdGroupsPortsUnderTheGitRoot is the demo for step 1A.10: two listeners
// started inside one checkout — one at its root, one in a subdirectory — carry
// the working directory they were started in, resolve a project_root at the
// checkout, and land in the same `<repo>` group in `sonar list --tree` without
// any config file claiming them.
func TestCwdGroupsPortsUnderTheGitRoot(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)

	// A checkout is a directory with a `.git` entry — groups.Find only ever
	// Lstats it — so no git binary is needed to make one.
	repoName := "sonar-cwd-demo"
	repo := filepath.Join(e.home, repoName)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately no .sonar.yaml: the group under test is the git-root one.
	if _, err := os.Stat(filepath.Join(repo, groups.ConfigName)); err == nil {
		t.Fatalf("%s exists; this test is about grouping without one", groups.ConfigName)
	}

	e.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	c := e.connect(ctx)
	// A subscriber keeps the daemon's scan loop running: with nobody listening
	// it parks between requests.
	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Buffer: 256})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}
	go drain(ctx, sub)

	rootPort, nestedPort := unusedPort(t), unusedPort(t)
	startListener(t, listener, repo, rootPort)
	startListener(t, listener, nested, nestedPort)

	wantCwd := map[int]string{
		rootPort:   groups.Canonical(repo),
		nestedPort: groups.Canonical(nested),
	}
	wantRoot := groups.Canonical(repo)

	rows := waitForCwds(t, ctx, c, wantCwd, 90*time.Second)

	for port, want := range wantCwd {
		p := rows[port]
		if p.Cwd == "" {
			t.Errorf("port %d has no cwd; the scanner cannot read this platform's per-process working directory", port)
			continue
		}
		// The raw field is compared canonically because each platform spells
		// the same directory its own way: /private/var on macOS, the long path
		// on Windows, the resolved symlink on Linux.
		if got := groups.Canonical(p.Cwd); got != want {
			t.Errorf("port %d cwd = %q (canonical %q), want %q", port, p.Cwd, got, want)
		}
		// Spellings the rest of sonar must never see, whatever the platform.
		if strings.HasPrefix(p.Cwd, `\\?\`) {
			t.Errorf("port %d cwd = %q keeps the extended-length prefix", port, p.Cwd)
		}
		if len(p.Cwd) > 1 && strings.HasSuffix(p.Cwd, string(filepath.Separator)) {
			t.Errorf("port %d cwd = %q keeps a trailing separator", port, p.Cwd)
		}

		if p.ProjectRoot == nil || *p.ProjectRoot == "" {
			t.Errorf("port %d has no project_root even though its cwd is %q", port, p.Cwd)
		} else if got := groups.Canonical(*p.ProjectRoot); got != wantRoot {
			t.Errorf("port %d project_root = %q (canonical %q), want the checkout %q",
				port, *p.ProjectRoot, got, wantRoot)
		}

		if p.Group == nil || *p.Group != repoName {
			t.Errorf("port %d group = %v, want %q from the git root", port, deref(p.Group), repoName)
		}
		if p.GroupSource == nil || *p.GroupSource != state.SourceAuto {
			t.Errorf("port %d group_source = %v, want %q (no .sonar.yaml claims it)",
				port, deref((*string)(p.GroupSource)), state.SourceAuto)
		}
	}

	// And the demo itself: the tree puts both under one heading named after the
	// repository.
	//
	// The CLI's read is only as fresh as the daemon's last publish: `sonar
	// list` is answered from the shared snapshot, so a listener ports.list has
	// already reported can still be missing from a tree rendered a moment
	// later, until the next scan tick lands. The render is therefore polled
	// until both ports are under the heading, and the last output is what a
	// failure prints.
	treeOut, section := waitForTree(t, e, repo, repoName, rootPort, nestedPort)
	t.Logf("sonar list --tree:\n%s", treeOut)
	if section == nil {
		// The rows are in the daemon — waitForCwds proved it — so a group
		// missing from the tree is the display filter, not the scan. `list`
		// hides desktop apps and `list -a` does not, and is_app is the field
		// that decides it, so print the rows the filter judged.
		reportPortRows(t, e, repo, rootPort, nestedPort)
		reportGroupDiagnostics(t, e, repo, "", nil)
		t.Fatalf("no %q group in the tree:\n%s", repoName, treeOut)
	}
	for _, port := range []int{rootPort, nestedPort} {
		if !sectionHasPort(section, port) {
			t.Errorf("port %d is not under the %q group:\n%s", port, repoName, treeOut)
		}
	}
}

// waitForTree renders `sonar list --tree` until the repository's heading holds
// every wanted port, or the timeout expires. It returns the last output and the
// section found in it, so the caller's assertions report the real thing rather
// than a snapshot that was merely early.
func waitForTree(t *testing.T, e *env, dir, name string, wanted ...int) ([]byte, []string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		tree := e.command("list", "--tree")
		tree.Dir = dir
		out, err := tree.CombinedOutput()
		if err != nil {
			t.Fatalf("sonar list --tree: %v\n%s", err, out)
		}
		section := treeSection(string(out), name)
		if section != nil && hasAllPorts(section, wanted) {
			return out, section
		}
		if !time.Now().Before(deadline) {
			return out, section
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func hasAllPorts(section []string, ports []int) bool {
	for _, port := range ports {
		if !sectionHasPort(section, port) {
			return false
		}
	}
	return true
}

// reportPortRows prints everything about the spawned listeners that decides
// whether they are displayed: their identity, what the OS said about them, and
// the is_app verdict that follows from it.
func reportPortRows(t *testing.T, e *env, dir string, wanted ...int) {
	t.Helper()
	cmd := e.command("list", "-a", "--json")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("`sonar list -a --json` failed: %v\n%s", err, out)
		return
	}
	var rows []state.Port
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Logf("decoding `sonar list -a --json`: %v\n%s", err, out)
		return
	}
	want := map[int]bool{}
	for _, p := range wanted {
		want[p] = true
	}
	for _, p := range rows {
		if !want[p.Port] {
			continue
		}
		t.Logf("port %d: is_app=%v process=%q command=%q cwd=%q project_root=%v group=%v type=%s",
			p.Port, p.IsApp, p.Process, p.Command, p.Cwd, deref(p.ProjectRoot), deref(p.Group), p.Type)
	}
}

// startListener spawns the helper in dir and registers its cleanup. dir, not
// the test's own working directory, is what the scanner has to read back.
func startListener(t *testing.T, listener, dir string, port int) {
	t.Helper()
	cmd := exec.Command(listener, strconv.Itoa(port))
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	var out safeBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the listener in %s: %v", dir, err)
	}
	t.Cleanup(func() {
		stopCommand(cmd)
		if t.Failed() && out.String() != "" {
			t.Logf("listener on %d:\n%s", port, out.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if portOpen(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the listener never came up on %d:\n%s", port, out.String())
}

// waitForCwds polls ports.list until every wanted port is present with a
// non-empty cwd, then returns the rows. The scan that first sees a port can
// race the process's own startup, so a row without a cwd is retried rather than
// failed on; the timeout is the real assertion.
func waitForCwds(t *testing.T, ctx context.Context, c *client.Client, want map[int]string, timeout time.Duration) map[int]state.Port {
	t.Helper()
	rows := map[int]state.Port{}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var res rpc.PortsListResult
		if err := c.Call(ctx, "ports.list", rpc.PortsListParams{All: true}, &res); err != nil {
			t.Fatalf("ports.list: %v", err)
		}
		found := 0
		for _, p := range res.Ports {
			if _, ok := want[p.Port]; !ok {
				continue
			}
			rows[p.Port] = p
			if p.Cwd != "" {
				found++
			}
		}
		if found == len(want) {
			return rows
		}
		time.Sleep(500 * time.Millisecond)
	}
	for port := range want {
		if p, ok := rows[port]; !ok {
			t.Errorf("the daemon never reported port %d at all", port)
		} else {
			t.Errorf("port %d never got a cwd; last row: %s", port, mustJSON(p))
		}
	}
	t.Fatalf("no cwd on the spawned listeners within %s", timeout)
	return rows
}

// drain keeps a subscription alive: a subscriber that stops reading is dropped,
// and the scan loop parks again with it.
func drain(ctx context.Context, sub *client.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-sub.Deltas:
			if !ok {
				return
			}
		case _, ok := <-sub.Events:
			if !ok {
				return
			}
		}
	}
}

// treeSection returns the lines `sonar list --tree` printed under the heading
// for name: the heading itself is the only line in a block that does not start
// with a branch character.
func treeSection(out, name string) []string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if fields := strings.Fields(line); len(fields) == 0 || fields[0] != name {
			continue
		}
		section := []string{lines[i]}
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "├─") && !strings.HasPrefix(next, "└─") {
				break
			}
			section = append(section, next)
		}
		return section
	}
	return nil
}

func sectionHasPort(section []string, port int) bool {
	want := strconv.Itoa(port)
	for _, line := range section[1:] {
		for _, f := range strings.Fields(line) {
			if f == want {
				return true
			}
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(b)
}
