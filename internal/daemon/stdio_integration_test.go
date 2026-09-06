//go:build integration

// Integration tests for `sonar daemon stdio`, the far end of a remote host's
// bridge. They run the real binary as a subprocess and speak JSON-RPC over its
// pipes — the same thing the local daemon does through ssh, minus the ssh.
package daemon_test

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// stdioStream is a spawned `sonar daemon stdio` seen as one bidirectional
// stream, which is exactly how the bridge sees it.
type stdioStream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
}

func (s *stdioStream) Read(b []byte) (int, error)  { return s.out.Read(b) }
func (s *stdioStream) Write(b []byte) (int, error) { return s.in.Write(b) }
func (s *stdioStream) Close() error {
	_ = s.in.Close()
	_ = s.out.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
	return nil
}

// startStdio runs `sonar daemon stdio` against this env's socket.
func (e *env) startStdio(t *testing.T) *stdioStream {
	t.Helper()
	cmd := e.command("daemon", "stdio")
	cmd.WaitDelay = waitDelay
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = testWriter{t}
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting `sonar daemon stdio`: %v", err)
	}
	s := &stdioStream{cmd: cmd, in: in, out: out}
	t.Cleanup(func() { s.Close() })
	return s
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Logf("daemon stdio: %s", b)
	return len(b), nil
}

// TestStdioSpeaksTheSameProtocol is the bridge's contract with the far side:
// the stream carries the ordinary daemon protocol, handshake, reads and
// subscriptions included.
func TestStdioSpeaksTheSameProtocol(t *testing.T) {
	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := client.Attach(ctx, e.startStdio(t), "stdio", client.ClientInfo{
		Name: "daemon", Version: "itest",
	})
	if err != nil {
		t.Fatalf("handshake over stdio: %v", err)
	}
	defer c.Close()

	hello := c.Hello()
	if hello.ProtocolVersion != rpc.ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", hello.ProtocolVersion, rpc.ProtocolVersion)
	}
	if hello.Socket != e.socket {
		t.Errorf("socket = %q, want the daemon this bridge proxied to (%q)", hello.Socket, e.socket)
	}

	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		t.Fatalf("state.snapshot over stdio: %v", err)
	}
	if len(snap.Hosts) == 0 || snap.Hosts[0].Name != state.LocalhostName {
		t.Errorf("hosts = %+v, want the far side's localhost row", snap.Hosts)
	}
	for _, p := range snap.Ports {
		if p.Host != state.LocalhostName {
			t.Errorf("the far side tagged a row %q; it should only know localhost", p.Host)
		}
	}

	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe over stdio: %v", err)
	}
	if sub.Snapshot.DaemonVersion == "" {
		t.Error("the subscription snapshot has no daemon_version")
	}
}

// TestStdioSharesTheRunningDaemon proves the rule the spec fixes: the bridge
// does not start a second scanner beside the daemon already on that host, it
// proxies to it. Two stdio bridges and the socket all report one pid.
func TestStdioSharesTheRunningDaemon(t *testing.T) {
	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	direct := e.connect(ctx).Hello().PID

	for i := 0; i < 2; i++ {
		c, err := client.Attach(ctx, e.startStdio(t), "stdio", client.ClientInfo{
			Name: "daemon", Version: "itest",
		})
		if err != nil {
			t.Fatalf("handshake over stdio: %v", err)
		}
		if got := c.Hello().PID; got != direct {
			t.Errorf("stdio bridge %d reported pid %d, want the running daemon's %d", i, got, direct)
		}
		c.Close()
	}
}

// TestStdioAutostartsTheDaemon is the spec's autostart rule: a bridge to a host
// where nothing is running starts the daemon rather than failing.
func TestStdioAutostartsTheDaemon(t *testing.T) {
	e := newEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := client.Attach(ctx, e.startStdio(t), "stdio", client.ClientInfo{
		Name: "daemon", Version: "itest",
	})
	if err != nil {
		t.Fatalf("handshake over stdio with no daemon running: %v", err)
	}
	pid := c.Hello().PID
	c.Close()
	t.Cleanup(func() { e.stopDaemon(pid) })

	if pid <= 0 {
		t.Errorf("pid = %d, want the autostarted daemon's", pid)
	}
}

// TestStdioRefusesWithNoAutostart covers the opt-out.
func TestStdioRefusesWithNoAutostart(t *testing.T) {
	e := newEnv(t)
	cmd := e.command("daemon", "stdio", "--no-autostart")
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("want a failure with no daemon running, got:\n%s", out)
	}
}
