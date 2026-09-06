//go:build integration

// Integration coverage for slice 2A.4: a real `sonar start` under an agent's
// environment, a real listener, and the sessions namespace answering about it.
// Run with `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/sessions"
	"github.com/raskrebs/sonar/internal/state"
)

// TestSessionAttributionEndToEnd is the step's acceptance demo in one test: a
// listener started under CLAUDE_CODE_SESSION_ID is attributed to that session,
// `sessions.list` shows it with its worktree and branch, `ports.list
// {session}` narrows to its ports, and `sessions.kill` stops it and leaves the
// session inactive.
func TestSessionAttributionEndToEnd(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	repo := gitCheckout(t, e.home, "shop", "feature/x")
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := e.connect(ctx)

	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	const sessionID = "itest-session-1"
	port := freePort(t)
	start := e.command("start", "--group", "itest", "--name", "web",
		"--port", strconv.Itoa(port), "--", listener, strconv.Itoa(port))
	start.Dir = repo
	start.Env = append(start.Env,
		"CLAUDE_CODE_SESSION_ID="+sessionID,
		"SONAR_SESSION_LABEL=acceptance demo")
	var out safeBuffer
	start.Stdout, start.Stderr = &out, &out
	ownProcessGroup(start)
	if err := start.Start(); err != nil {
		t.Fatalf("sonar start: %v", err)
	}
	t.Cleanup(func() {
		stopCommand(start)
		if t.Failed() {
			t.Logf("sonar start output:\n%s", out.String())
		}
	})

	added, ok := waitForAttributedPort(t, sub, port, 60*time.Second)
	if !ok {
		t.Fatalf("no delta carried port %d with its run within 60s\n%s", port, out.String())
	}
	if added.Session == nil {
		t.Fatalf("port %d was published with no session\n%s", port, out.String())
	}
	if added.Session.ID != sessionID {
		t.Errorf("session id = %q, want %q", added.Session.ID, sessionID)
	}
	if added.Session.Tool != sessions.ToolClaudeCode {
		t.Errorf("tool = %q, want %s", added.Session.Tool, sessions.ToolClaudeCode)
	}
	if !added.Session.Detected {
		t.Error("a session recognised from CLAUDE_CODE_SESSION_ID must be marked detected")
	}
	if added.Session.Branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", added.Session.Branch)
	}
	if added.Session.Label != "acceptance demo" {
		t.Errorf("label = %q", added.Session.Label)
	}

	// sessions.list sees it, with the run and the port counted.
	var list rpc.SessionsListResult
	if err := c.Call(ctx, "sessions.list", rpc.SessionsListParams{ActiveOnly: true}, &list); err != nil {
		t.Fatalf("sessions.list: %v", err)
	}
	rec, found := findRecord(list.Sessions, sessionID)
	if !found {
		t.Fatalf("sessions.list did not carry %s: %+v", sessionID, list.Sessions)
	}
	if rec.Runs < 1 || rec.Ports < 1 || !rec.Active {
		t.Errorf("record = %+v, want an active session with a run and a port", rec)
	}
	if rec.Branch != "feature/x" {
		t.Errorf("record branch = %q", rec.Branch)
	}

	// sessions.inspect lists the run and the port.
	var inspect rpc.SessionsInspectResult
	if err := c.Call(ctx, "sessions.inspect", rpc.SessionsInspectParams{ID: sessionID}, &inspect); err != nil {
		t.Fatalf("sessions.inspect: %v", err)
	}
	if len(inspect.Runs) != 1 || len(inspect.Ports) != 1 || inspect.Ports[0].Port != port {
		t.Errorf("inspect = %d runs, %d ports", len(inspect.Runs), len(inspect.Ports))
	}

	// ports.list narrows to the session, and rejects an unknown one by
	// returning nothing rather than everything.
	var scoped rpc.PortsListResult
	id := sessionID
	if err := c.Call(ctx, "ports.list", rpc.PortsListParams{All: true, Session: &id}, &scoped); err != nil {
		t.Fatalf("ports.list --session: %v", err)
	}
	if len(scoped.Ports) != 1 || scoped.Ports[0].Port != port {
		t.Errorf("ports.list --session = %+v, want only port %d", scoped.Ports, port)
	}
	other := "nobody"
	if err := c.Call(ctx, "ports.list", rpc.PortsListParams{All: true, Session: &other}, &scoped); err != nil {
		t.Fatalf("ports.list --session nobody: %v", err)
	}
	if len(scoped.Ports) != 0 {
		t.Errorf("an unknown session matched %d ports", len(scoped.Ports))
	}

	// sessions.kill stops everything the session started.
	var env rpc.KillEnvelope
	if err := c.Call(ctx, "sessions.kill", rpc.SessionsKillParams{ID: sessionID}, &env); err != nil {
		t.Fatalf("sessions.kill: %v", err)
	}
	if !env.OK || len(env.Results) == 0 {
		t.Fatalf("sessions.kill envelope = %+v", env)
	}
	if !waitForRemoved(t, sub, port, 45*time.Second) {
		t.Fatalf("port %d never went away after sessions.kill", port)
	}

	// And the session goes inactive: it is still known, with nothing running.
	deadline := time.Now().Add(30 * time.Second)
	for {
		var after rpc.SessionsListResult
		if err := c.Call(ctx, "sessions.list", rpc.SessionsListParams{}, &after); err != nil {
			t.Fatalf("sessions.list after the kill: %v", err)
		}
		rec, found := findRecord(after.Sessions, sessionID)
		if found && !rec.Active && rec.Runs == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never went inactive: %+v", sessionID, after.Sessions)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// active_only now hides it.
	var active rpc.SessionsListResult
	if err := c.Call(ctx, "sessions.list", rpc.SessionsListParams{ActiveOnly: true}, &active); err != nil {
		t.Fatalf("sessions.list active_only: %v", err)
	}
	if _, found := findRecord(active.Sessions, sessionID); found {
		t.Errorf("active_only still lists the finished session: %+v", active.Sessions)
	}
}

// TestStartWithoutAnAgentHasNoSession is the other half of the contract: a
// plain shell must not manufacture a session for every dev server.
func TestStartWithoutAnAgentHasNoSession(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := e.connect(ctx)

	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	port := freePort(t)
	start := e.command("start", "--group", "plain", "--name", "web",
		"--port", strconv.Itoa(port), "--", listener, strconv.Itoa(port))
	start.Dir = e.home
	start.Env = append(start.Env, "CLAUDECODE=", "CLAUDE_CODE_SESSION_ID=",
		"CLAUDE_SESSION_ID=", "CODEX_THREAD_ID=", "CODEX_SANDBOX=",
		"CURSOR_AGENT=", "SONAR_SESSION=", "SONAR_SESSION_ID=")
	var out safeBuffer
	start.Stdout, start.Stderr = &out, &out
	ownProcessGroup(start)
	if err := start.Start(); err != nil {
		t.Fatalf("sonar start: %v", err)
	}
	t.Cleanup(func() { stopCommand(start) })

	added, ok := waitForAttributedPort(t, sub, port, 45*time.Second)
	if !ok {
		t.Fatalf("no delta carried port %d\n%s", port, out.String())
	}
	if added.Session != nil {
		t.Errorf("a shell-started run got session %+v", *added.Session)
	}

	var list rpc.SessionsListResult
	if err := c.Call(ctx, "sessions.list", rpc.SessionsListParams{}, &list); err != nil {
		t.Fatalf("sessions.list: %v", err)
	}
	if len(list.Sessions) != 0 {
		t.Errorf("sessions.list = %+v, want none", list.Sessions)
	}
}

func findRecord(recs []state.SessionRecord, id string) (state.SessionRecord, bool) {
	for _, r := range recs {
		if r.ID == id {
			return r, true
		}
	}
	return state.SessionRecord{}, false
}

// gitCheckout makes a real repository under home with one commit on branch,
// because the session's branch is read from .git.
func gitCheckout(t *testing.T, home, name, branch string) string {
	t.Helper()
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, strings.TrimSpace(string(out)))
		}
	}
	run("init", "-q", "-b", branch, ".")
	run("commit", "-q", "--allow-empty", "-m", "first")
	return dir
}
