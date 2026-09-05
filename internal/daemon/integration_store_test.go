//go:build integration

// Spec integration test 3: a rename is applied to the published state and
// survives a daemon restart, because it lives in the database rather than in
// the scanner's memory. Run with `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

func TestRenameSurvivesADaemonRestart(t *testing.T) {
	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("opening a test listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	t.Logf("test listener is on port %d", port)

	c := e.connect(ctx)
	if !waitForPort(t, ctx, c, port, 30*time.Second) {
		t.Fatalf("the daemon never saw port %d", port)
	}

	// The daemon has a database, and says where.
	var status rpc.DaemonStatusResult
	if err := c.Call(ctx, "daemon.status", rpc.Empty{}, &status); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	if !strings.HasSuffix(status.DBPath, "sonar.db") {
		t.Errorf("db_path = %q, want the sonar.db under the temp home", status.DBPath)
	}
	if !strings.HasPrefix(status.DBPath, e.home) {
		t.Errorf("db_path = %q, want it inside %s", status.DBPath, e.home)
	}

	// Rename through the CLI, the way a user does.
	out, err := e.command("rename", strconv.Itoa(port), "storefront").CombinedOutput()
	if err != nil {
		t.Fatalf("sonar rename: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "storefront") {
		t.Errorf("`sonar rename` said %q, want it to name the new name", out)
	}

	if got := displayNameOf(t, ctx, c, port); got != "storefront" {
		t.Fatalf("display_name = %q right after the rename, want storefront", got)
	}
	c.Close()

	// Restart: a new process, a new scanner, the same database.
	if out, err := e.command("daemon", "stop").CombinedOutput(); err != nil {
		t.Fatalf("sonar daemon stop: %v\n%s", err, out)
	}
	e.serve()
	c2 := e.connect(ctx)

	if !waitForPort(t, ctx, c2, port, 30*time.Second) {
		t.Fatalf("the restarted daemon never saw port %d", port)
	}
	if got := displayNameOf(t, ctx, c2, port); got != "storefront" {
		t.Fatalf("display_name = %q after the restart, want storefront", got)
	}

	// The port coming up was recorded, and `sonar history` can read it back.
	var hist rpc.PortsHistoryResult
	if err := c2.Call(ctx, "ports.history", rpc.PortsHistoryParams{Port: &port}, &hist); err != nil {
		t.Fatalf("ports.history: %v", err)
	}
	if len(hist.Events) == 0 {
		t.Fatalf("no history rows for port %d", port)
	}
	found := false
	for _, ev := range hist.Events {
		if ev.Kind == "port_up" && ev.Port == port {
			found = true
		}
	}
	if !found {
		t.Errorf("history = %+v, want a port_up row for %d", hist.Events, port)
	}

	histOut, err := e.command("history", strconv.Itoa(port)).CombinedOutput()
	if err != nil {
		t.Fatalf("sonar history: %v\n%s", err, histOut)
	}
	if !strings.Contains(string(histOut), "port_up") {
		t.Errorf("`sonar history %d` = %q, want a port_up row", port, histOut)
	}
}

func TestAssignPinsAGroupThroughTheCLI(t *testing.T) {
	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("opening a test listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	c := e.connect(ctx)
	if !waitForPort(t, ctx, c, port, 30*time.Second) {
		t.Fatalf("the daemon never saw port %d", port)
	}

	out, err := e.command("assign", strconv.Itoa(port), "itest").CombinedOutput()
	if err != nil {
		t.Fatalf("sonar assign: %v\n%s", err, out)
	}

	p := portFrom(t, ctx, c, port)
	if p.Group == nil || *p.Group != "itest" {
		t.Fatalf("group = %v, want itest", p.Group)
	}
	if p.GroupSource == nil || *p.GroupSource != state.SourceManual {
		t.Errorf("group_source = %v, want manual", p.GroupSource)
	}

	var groupSeen bool
	snap := snapshot(t, ctx, c)
	for _, g := range snap.Groups {
		if g.Name == "itest" {
			groupSeen = true
		}
	}
	if !groupSeen {
		t.Errorf("groups = %+v, want the pinned group published", snap.Groups)
	}

	if out, err := e.command("assign", strconv.Itoa(port), "--clear").CombinedOutput(); err != nil {
		t.Fatalf("sonar assign --clear: %v\n%s", err, out)
	}
	p = portFrom(t, ctx, c, port)
	if p.Group != nil && *p.Group == "itest" {
		t.Errorf("group = itest after --clear, want the inferred group back")
	}
}

func snapshot(t *testing.T, ctx context.Context, c *client.Client) state.Snapshot {
	t.Helper()
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	return snap
}

func portFrom(t *testing.T, ctx context.Context, c *client.Client, port int) state.Port {
	t.Helper()
	for _, p := range snapshot(t, ctx, c).Ports {
		if p.Port == port {
			return p
		}
	}
	t.Fatalf("port %d is not in the snapshot", port)
	return state.Port{}
}

func displayNameOf(t *testing.T, ctx context.Context, c *client.Client, port int) string {
	t.Helper()
	// The rename lands on the next scan; the write triggers one, but give the
	// snapshot cache a couple of ticks rather than racing it.
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = portFrom(t, ctx, c, port).DisplayName
		if last == "storefront" {
			return last
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last
}

func waitForPort(t *testing.T, ctx context.Context, c *client.Client, port int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var snap state.Snapshot
		if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
			t.Fatalf("state.snapshot: %v", err)
		}
		for _, p := range snap.Ports {
			if p.Port == port {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
