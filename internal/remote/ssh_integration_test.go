//go:build integration

// The whole remote-host path with no network and no SSH: a stand-in `ssh` on
// PATH execs the real binary's `daemon stdio` against a second daemon on this
// machine, so `remote.add` → rows appear → `remote.remove` → rows vanish is
// exercised end to end through the real spawn, the real bridge and the real
// multiplexer.
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// buildBinary compiles the sonar CLI once per test run.
var buildBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "sonar-remote-itest-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "sonar")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building sonar: %v: %s", err, out)
	}
	return bin, nil
})

// shortTempDir is a temp directory outside t.TempDir(), whose path is short
// enough for a unix socket on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "snr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "."
}

// fakeSSHScript is the stand-in for ssh. It throws away every ssh option and
// the remote command, and runs this build's `daemon stdio` against the second
// daemon's socket — which is precisely the shape of the real thing: ssh is a
// pipe to a `sonar daemon stdio` somewhere else.
const fakeSSHScript = `#!/bin/sh
export HOME="$FAKE_SSH_HOME"
export USERPROFILE="$FAKE_SSH_HOME"
export SONAR_SOCKET="$FAKE_SSH_SOCKET"
export XDG_RUNTIME_DIR=
exec "$FAKE_SSH_BIN" daemon stdio
`

// bridgeEnv is a local daemon plus a "remote" daemon on the same machine, with
// a fake ssh joining them.
type bridgeEnv struct {
	t           *testing.T
	bin         string
	localHome   string
	remoteHome  string
	localSocket string
	remoteSock  string
	binDir      string
	serveCmd    *exec.Cmd
}

func newBridgeEnv(t *testing.T) *bridgeEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script; the bridge itself is covered by the unit tests")
	}
	bin, err := buildBinary()
	if err != nil {
		t.Fatal(err)
	}

	// macOS caps a unix socket path at ~104 bytes, so the sockets live in
	// short directories of their own rather than under t.TempDir(). One
	// directory each: the single-instance lock sits beside the socket
	// (contract §15), so two daemons sharing a directory would fight over one
	// lock and the second would never start.
	e := &bridgeEnv{
		t:           t,
		bin:         bin,
		localHome:   t.TempDir(),
		remoteHome:  t.TempDir(),
		localSocket: filepath.Join(shortTempDir(t), "d.sock"),
		remoteSock:  filepath.Join(shortTempDir(t), "d.sock"),
		binDir:      t.TempDir(),
	}

	script := filepath.Join(e.binDir, "ssh")
	if err := os.WriteFile(script, []byte(fakeSSHScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *bridgeEnv) env() []string {
	return append(os.Environ(),
		"HOME="+e.localHome,
		"USERPROFILE="+e.localHome,
		"SONAR_SOCKET="+e.localSocket,
		"XDG_RUNTIME_DIR=",
		"PATH="+e.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_SSH_BIN="+e.bin,
		"FAKE_SSH_HOME="+e.remoteHome,
		"FAKE_SSH_SOCKET="+e.remoteSock,
	)
}

// serve starts the local daemon: the one that owns the bridges.
func (e *bridgeEnv) serve() {
	e.t.Helper()
	cmd := exec.Command(e.bin, "serve")
	cmd.Env = e.env()
	cmd.WaitDelay = 5 * time.Second
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		e.t.Fatalf("starting the local daemon: %v", err)
	}
	e.serveCmd = cmd
	e.t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		e.stopRemoteDaemon()
		if e.t.Failed() && out.Len() > 0 {
			e.t.Logf("local daemon output:\n%s", out.String())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.WaitForSocket(ctx, e.localSocket, 15*time.Second); err != nil {
		e.t.Fatalf("the local daemon never came up: %v\n%s", err, out.String())
	}
}

// stopRemoteDaemon shuts down the daemon the bridge autostarted on the "remote"
// side, so its lock and log are released before t.TempDir cleans up.
func (e *bridgeEnv) stopRemoteDaemon() {
	cmd := exec.Command(e.bin, "daemon", "stop")
	cmd.Env = append(os.Environ(),
		"HOME="+e.remoteHome,
		"USERPROFILE="+e.remoteHome,
		"SONAR_SOCKET="+e.remoteSock,
		"XDG_RUNTIME_DIR=",
	)
	cmd.WaitDelay = 5 * time.Second
	_ = cmd.Run()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !daemon.SocketAlive(e.remoteSock) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *bridgeEnv) connect(ctx context.Context) *client.Client {
	e.t.Helper()
	c, err := client.Dial(ctx, client.ClientInfo{
		Name: "cli", Version: "itest", Socket: e.localSocket,
	})
	if err != nil {
		e.t.Fatalf("connecting to the local daemon: %v", err)
	}
	e.t.Cleanup(func() { c.Close() })
	return c
}

// TestRemoteAddBringsRowsInAndRemoveTakesThemOut is the step's acceptance
// demo, minus the network.
func TestRemoteAddBringsRowsInAndRemoveTakesThemOut(t *testing.T) {
	e := newBridgeEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := e.connect(ctx)

	if !hasCapability(c.Hello().Capabilities, "remote") {
		t.Errorf("capabilities = %v, want remote announced", c.Hello().Capabilities)
	}

	// Something has to be listening for the bridge to carry, and both daemons
	// scan this same machine, so one listener shows up on both sides.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	var added rpc.RemoteAddResult
	if err := c.Call(ctx, "remote.add", rpc.RemoteAddParams{
		Target: "deploy@fake", Name: "fake",
	}, &added); err != nil {
		t.Fatalf("remote.add: %v", err)
	}
	if !added.OK || added.Host.Name != "fake" {
		t.Fatalf("remote.add result = %+v", added)
	}

	// The bridge connects.
	waitUntil(t, ctx, "the host to connect", func() bool {
		var list rpc.RemoteListResult
		if err := c.Call(ctx, "remote.list", rpc.Empty{}, &list); err != nil {
			return false
		}
		for _, h := range list.Hosts {
			if h.Name == "fake" && h.Status == state.HostConnected {
				if h.DaemonVersion == "" {
					t.Errorf("connected host has no daemon_version: %+v", h)
				}
				return true
			}
		}
		return false
	})

	// Its rows reach the local stream, tagged and prefixed.
	waitUntil(t, ctx, "the remote port to be multiplexed in", func() bool {
		snap := snapshot(t, ctx, c, []string{"*"})
		for _, p := range snap.Ports {
			if p.Host == "fake" && p.Port == port {
				if want := fmt.Sprintf("fake/%d:%s", port, p.BindAddress); p.Key() != want {
					t.Errorf("remote key = %q, want %q", p.Key(), want)
				}
				return true
			}
		}
		return false
	})

	// The same port is there once as a local row, with its unprefixed key.
	snap := snapshot(t, ctx, c, []string{"*"})
	local, remote := 0, 0
	for _, p := range snap.Ports {
		if p.Port != port {
			continue
		}
		switch p.Host {
		case state.LocalhostName:
			local++
			if strings.Contains(p.Key(), "/") {
				t.Errorf("local key %q is namespaced; decision 1 says it must not be", p.Key())
			}
		case "fake":
			remote++
		}
	}
	if local == 0 || remote == 0 {
		t.Errorf("port %d appeared %d times locally and %d times remotely, want both", port, local, remote)
	}

	// A default subscriber never saw any of it.
	localOnly := snapshot(t, ctx, c, nil)
	for _, p := range localOnly.Ports {
		if p.Host != state.LocalhostName {
			t.Errorf("the default view leaked a %q row", p.Host)
		}
	}

	if err := c.Call(ctx, "remote.remove", rpc.RemoteRemoveParams{Name: "fake"}, nil); err != nil {
		t.Fatalf("remote.remove: %v", err)
	}

	waitUntil(t, ctx, "the remote rows to vanish", func() bool {
		snap := snapshot(t, ctx, c, []string{"*"})
		for _, p := range snap.Ports {
			if p.Host == "fake" {
				return false
			}
		}
		for _, h := range snap.Hosts {
			if h.Name == "fake" {
				return false
			}
		}
		return true
	})

	var list rpc.RemoteListResult
	if err := c.Call(ctx, "remote.list", rpc.Empty{}, &list); err != nil {
		t.Fatalf("remote.list: %v", err)
	}
	if len(list.Hosts) != 0 {
		t.Errorf("remote.list = %+v, want no hosts after remove", list.Hosts)
	}
}

// TestRemoteCallForwardsAWrite covers decision 3: a write goes over the bridge
// like any other method, and the remote daemon applies it.
func TestRemoteCallForwardsAWrite(t *testing.T) {
	e := newBridgeEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := e.connect(ctx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if err := c.Call(ctx, "remote.add", rpc.RemoteAddParams{Target: "deploy@fake", Name: "fake"}, nil); err != nil {
		t.Fatalf("remote.add: %v", err)
	}
	waitUntil(t, ctx, "the remote port to be multiplexed in", func() bool {
		for _, p := range snapshot(t, ctx, c, []string{"*"}).Ports {
			if p.Host == "fake" && p.Port == port {
				return true
			}
		}
		return false
	})

	name := "renamed-by-the-bridge"
	var renamed rpc.PortsRenameResult
	if err := c.Call(ctx, "remote.call", rpc.RemoteCallParams{
		Host:   "fake",
		Method: "ports.rename",
		Params: mustJSON(t, rpc.PortsRenameParams{
			Selector: rpc.Selector{Port: &port},
			Name:     &name,
		}),
	}, &renamed); err != nil {
		t.Fatalf("remote.call ports.rename: %v", err)
	}
	if renamed.Name == nil || *renamed.Name != name {
		t.Fatalf("rename result = %+v, want the remote's own reply", renamed)
	}

	waitUntil(t, ctx, "the rename to reach the multiplexed rows", func() bool {
		for _, p := range snapshot(t, ctx, c, []string{"*"}).Ports {
			if p.Host == "fake" && p.Port == port && p.Name != nil && *p.Name == name {
				return true
			}
		}
		return false
	})

	// The local row is untouched: the write went to the other machine.
	for _, p := range snapshot(t, ctx, c, nil).Ports {
		if p.Port == port && p.Name != nil && *p.Name == name {
			t.Error("the forwarded rename also renamed the local row")
		}
	}
}

// TestRemoteCallOnAnUnknownHost keeps a typo readable.
func TestRemoteCallOnAnUnknownHost(t *testing.T) {
	e := newBridgeEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := e.connect(ctx)

	err := c.Call(ctx, "remote.call", rpc.RemoteCallParams{
		Host: "nope", Method: "ports.list",
	}, nil)
	if err == nil {
		t.Fatal("want an error for an unregistered host")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want it to name the host", err)
	}
}

func snapshot(t *testing.T, ctx context.Context, c *client.Client, hosts []string) state.Snapshot {
	t.Helper()
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{Hosts: hosts}, &snap); err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	return snap
}

func waitUntil(t *testing.T, ctx context.Context, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("context ended waiting for %s", what)
		}
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func hasCapability(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
