//go:build integration

// Acting on a remote host, end to end over the same fake-ssh bridge the 3A.2
// tests use: a kill, a group start with its stream, and a rename addressed by
// the delta key a client already holds. Two real daemons, the real spawn, the
// real relay — only the network is missing.
//
// Run with `go test -tags integration ./internal/remote/...`.
package remote_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// buildListener compiles a helper that listens on the port it is given and
// blocks. A kill needs a real process holding a real socket; the test process
// itself is not a candidate.
var buildListener = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "sonar-remote-itest-listener")
	if err != nil {
		return "", err
	}
	src := filepath.Join(dir, "main.go")
	program := `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer ln.Close()
	fmt.Println("listening on", os.Args[1])
	for {
		time.Sleep(time.Second)
	}
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "listener")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the listener helper: %v: %s", err, out)
	}
	return bin, nil
})

// freePort returns a port nothing is listening on right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// startListener runs the helper and waits until it is accepting.
func startListener(t *testing.T, port int) *exec.Cmd {
	t.Helper()
	bin, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, strconv.Itoa(port))
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the listener: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return cmd
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the listener never came up on %d", port)
	return nil
}

// register adds the fake host and waits for its bridge to connect.
func register(t *testing.T, ctx context.Context, c *client.Client, name string) {
	t.Helper()
	if err := c.Call(ctx, "remote.add", rpc.RemoteAddParams{Target: "deploy@fake", Name: name}, nil); err != nil {
		t.Fatalf("remote.add: %v", err)
	}
	waitUntil(t, ctx, "the host to connect", func() bool {
		var list rpc.RemoteListResult
		if err := c.Call(ctx, "remote.list", rpc.Empty{}, &list); err != nil {
			return false
		}
		for _, h := range list.Hosts {
			if h.Name == name && h.Status == state.HostConnected {
				return true
			}
		}
		return false
	})
}

// waitForRemotePort waits until a port shows up in the multiplexed rows under
// the given host, and returns its row.
func waitForRemotePort(t *testing.T, ctx context.Context, c *client.Client, host string, port int) state.Port {
	t.Helper()
	var found state.Port
	waitUntil(t, ctx, fmt.Sprintf("port %d to appear on %s", port, host), func() bool {
		for _, p := range snapshot(t, ctx, c, []string{"*"}).Ports {
			if p.Host == host && p.Port == port {
				found = p
				return true
			}
		}
		return false
	})
	return found
}

// TestKillAPortOnARemoteHost is the step's acceptance path: `ports.kill` with a
// host kills on that machine, and the envelope says so — the rows carry the
// host and `affected` carries the key the state stream uses for it.
func TestKillAPortOnARemoteHost(t *testing.T) {
	e := newBridgeEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := e.connect(ctx)
	register(t, ctx, c, "fake")

	port := freePort(t)
	listener := startListener(t, port)
	row := waitForRemotePort(t, ctx, c, "fake", port)

	var env rpc.KillEnvelope
	if err := c.Call(ctx, "ports.kill", rpc.PortsKillParams{
		HostParams: rpc.HostParams{Host: "fake"},
		Targets:    []rpc.Selector{{Port: &port}},
	}, &env); err != nil {
		t.Fatalf("ports.kill on a remote host: %v", err)
	}
	if !env.OK || len(env.Results) == 0 {
		t.Fatalf("kill envelope = %+v, want a successful kill", env)
	}
	for _, r := range env.Results {
		if r.Host != "fake" {
			t.Errorf("result row host = %q, want fake", r.Host)
		}
	}
	wantKey := fmt.Sprintf("fake/%d:%s", port, row.BindAddress)
	if len(env.Affected) != 1 || env.Affected[0] != wantKey {
		t.Errorf("affected = %v, want [%s] — the key the stream uses", env.Affected, wantKey)
	}

	// The process really is gone, and its row leaves the multiplexed state.
	if err := listener.Wait(); err == nil {
		t.Log("the listener exited cleanly")
	}
	waitUntil(t, ctx, "the killed port to leave the stream", func() bool {
		for _, p := range snapshot(t, ctx, c, []string{"*"}).Ports {
			if p.Host == "fake" && p.Port == port {
				return false
			}
		}
		return true
	})
}

// TestRenameOnARemoteHostByItsDeltaKey: a client hands back the key it was
// given, and that alone says which machine the row is on.
func TestRenameOnARemoteHostByItsDeltaKey(t *testing.T) {
	e := newBridgeEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := e.connect(ctx)
	register(t, ctx, c, "fake")

	port := freePort(t)
	startListener(t, port)
	row := waitForRemotePort(t, ctx, c, "fake", port)
	key := row.Key()

	name := "named-by-its-key"
	var res rpc.PortsRenameResult
	if err := c.Call(ctx, "ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Key: key},
		Name:     &name,
	}, &res); err != nil {
		t.Fatalf("ports.rename by key: %v", err)
	}
	// `affected` is where the port key lives; `key` is the store's own
	// identity for the rename and is not namespaced.
	if len(res.Affected) != 1 || res.Affected[0] != key {
		t.Errorf("affected = %v, want [%s] — the key the caller sent", res.Affected, key)
	}
	if res.Name == nil || *res.Name != name {
		t.Fatalf("rename result = %+v", res)
	}

	waitUntil(t, ctx, "the rename to reach the multiplexed rows", func() bool {
		for _, p := range snapshot(t, ctx, c, []string{"*"}).Ports {
			if p.Host == "fake" && p.Port == port && p.Name != nil && *p.Name == name {
				return true
			}
		}
		return false
	})

	// The write went to the other machine; the local row for the same port is
	// untouched.
	for _, p := range snapshot(t, ctx, c, nil).Ports {
		if p.Port == port && p.Name != nil && *p.Name == name {
			t.Error("the rename addressed by a remote key also renamed the local row")
		}
	}
}

// TestGroupsStartOnARemoteHostStreams: the group runs on the other machine and
// its chunks arrive here, one per service, under this connection's own
// subscription id.
func TestGroupsStartOnARemoteHostStreams(t *testing.T) {
	listenerBin, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	e := newBridgeEnv(t)
	// The project lives inside the *remote* daemon's home: it is that daemon
	// that reads the file and spawns the services.
	project := filepath.Join(e.remoteHome, "demo")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	apiPort, webPort := freePort(t), freePort(t)
	config := fmt.Sprintf(`name: demo
services:
  - name: api
    cmd: '%s %d'
    port: %d
  - name: web
    cmd: '%s %d'
    port: %d
`, listenerBin, apiPort, apiPort, listenerBin, webPort, webPort)
	configPath := filepath.Join(project, ".sonar.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	e.serve()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c := e.connect(ctx)
	register(t, ctx, c, "fake")
	t.Cleanup(func() {
		// Whatever the group started belongs to the other machine's daemon,
		// and outlives this test unless it is stopped.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = c.Call(stopCtx, "groups.kill", rpc.GroupsKillParams{
			HostParams: rpc.HostParams{Host: "fake"}, Name: "demo", Force: true,
		}, nil)
	})

	var start rpc.GroupsStartResult
	stream, err := c.Stream(ctx, "groups.start", rpc.GroupsStartParams{
		HostParams: rpc.HostParams{Host: "fake"},
		ConfigPath: &configPath,
	}, &start)
	if err != nil {
		t.Fatalf("groups.start on a remote host: %v", err)
	}
	defer stream.Close()
	if start.SubscriptionID == "" {
		t.Fatal("a relayed stream must reply with this daemon's own subscription id")
	}
	if stream.ID() != start.SubscriptionID {
		t.Errorf("stream id = %q, reply said %q", stream.ID(), start.SubscriptionID)
	}

	started := map[string]int{}
	for raw := range stream.Chunks() {
		var chunk rpc.GroupsStartChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatalf("decoding a relayed chunk: %v", err)
		}
		if chunk.Error != "" {
			t.Errorf("service %s failed on the remote host: %s", chunk.Service, chunk.Error)
			continue
		}
		started[chunk.Service] = chunk.PID
	}
	end := <-stream.End()
	if end.Err != nil {
		t.Fatalf("the relayed stream ended with an error: %v", end.Err)
	}
	var summary rpc.GroupsStartEnd
	if err := end.Decode(&summary); err != nil {
		t.Fatalf("decoding the relayed end: %v", err)
	}

	for _, svc := range []string{"api", "web"} {
		if _, ok := started[svc]; !ok {
			t.Errorf("no chunk arrived for %s; got %v", svc, started)
		}
	}
	if len(summary.Started)+len(summary.Skipped) != 2 || len(summary.Errors) != 0 {
		t.Errorf("summary = %+v, want both services accounted for", summary)
	}

	// And what it started is on the other machine, in the local stream.
	waitUntil(t, ctx, "the started services to reach the multiplexed rows", func() bool {
		seen := 0
		for _, p := range snapshot(t, ctx, c, []string{"*"}).Ports {
			if p.Host == "fake" && (p.Port == apiPort || p.Port == webPort) {
				seen++
			}
		}
		return seen >= 2
	})
}

// TestCancelReachesTheRemoteStream: `stream.cancel` on the local subscription
// id stops the work on the other machine, not just the forwarding here. The
// wait is given a minute; if the cancel did not travel, the stream would still
// be open when the test's own deadline runs out.
func TestCancelReachesTheRemoteStream(t *testing.T) {
	e := newBridgeEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := e.connect(ctx)
	register(t, ctx, c, "fake")

	// A port nothing will ever listen on, so the wait runs until it is told
	// to stop.
	stream, err := c.Stream(ctx, "ports.wait", rpc.PortsWaitParams{
		HostParams: rpc.HostParams{Host: "fake"},
		Ports:      []int{freePort(t)},
		TimeoutMs:  60_000,
		IntervalMs: 250,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait on a remote host: %v", err)
	}
	defer stream.Close()

	if err := stream.Cancel(ctx); err != nil {
		t.Fatalf("stream.cancel: %v", err)
	}
	select {
	case end := <-stream.End():
		if end.Err != nil {
			t.Fatalf("a cancelled stream must end cleanly, got %v", end.Err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the cancel never reached the remote stream: it is still running")
	}
}
