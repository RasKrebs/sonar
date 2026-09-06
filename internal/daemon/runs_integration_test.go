//go:build integration

// Integration coverage for `sonar start` and the daemon's runs registry: spec
// integration test 2. Run with `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
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
// blocks. It is a real process with a real socket, so the daemon's own scan has
// to find it and attribute it through the PPID chain.
var buildListener = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "sonar-itest-listener")
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
	cmd := exec.Command("go", "build", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
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
	ln.Close()
	return port
}

// TestStartAttributesItsPortToTheGroup is spec integration test 2: a helper
// started through `sonar start --group itest --name web` shows up in a delta
// carrying the group, the source and the name the run gave it.
func TestStartAttributesItsPortToTheGroup(t *testing.T) {
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
	start := e.command("start", "--group", "itest", "--name", "web", "--port", strconv.Itoa(port),
		"--", listener, strconv.Itoa(port))
	start.Dir = e.home
	var out safeBuffer
	start.Stdout, start.Stderr = &out, &out
	if err := start.Start(); err != nil {
		t.Fatalf("sonar start: %v", err)
	}
	t.Cleanup(func() {
		if start.Process != nil {
			_ = start.Process.Kill()
			_ = start.Wait()
		}
		if t.Failed() {
			t.Logf("sonar start output:\n%s", out.String())
		}
	})

	added, ok := waitForAttributedPort(t, sub, port, 45*time.Second)
	if !ok {
		t.Fatalf("no state.delta carried port %d with its run within 45s\n%s", port, out.String())
	}

	if added.Group == nil || *added.Group != "itest" {
		t.Errorf("group = %v, want itest", added.Group)
	}
	if added.GroupSource == nil || string(*added.GroupSource) != "start" {
		t.Errorf("group_source = %v, want start", added.GroupSource)
	}
	if added.DisplayName != "web" {
		t.Errorf("display_name = %q, want web", added.DisplayName)
	}
	if added.Run == nil {
		t.Fatalf("run is null; the port was not attributed to the run")
	}
	if added.Run.Name != "web" || added.Run.Group != "itest" {
		t.Errorf("run = %+v, want group itest name web", *added.Run)
	}
	if added.Run.RootPID <= 0 {
		t.Errorf("run.root_pid = %d, want the `sonar start` pid", added.Run.RootPID)
	}

	// The same run through runs.list, with the port it ended up binding.
	var list rpc.RunsListResult
	if err := c.Call(ctx, "runs.list", rpc.Empty{}, &list); err != nil {
		t.Fatalf("runs.list: %v", err)
	}
	found := false
	for _, r := range list.Runs {
		if r.Group != "itest" || r.Name != "web" {
			continue
		}
		found = true
		if r.Status != "running" {
			t.Errorf("status = %q, want running once the port is bound", r.Status)
		}
		if r.PortHint == nil || *r.PortHint != port {
			t.Errorf("port_hint = %v, want %d", r.PortHint, port)
		}
		if len(r.Ports) != 1 || r.Ports[0] != port {
			t.Errorf("ports = %v, want [%d]", r.Ports, port)
		}
	}
	if !found {
		t.Fatalf("runs.list did not carry the run: %+v", list.Runs)
	}

	// Interrupting `sonar start` takes the listener down with it, and the run
	// leaves the registry.
	interrupt(t, start)
	if !waitForRemoved(t, sub, port, 45*time.Second) {
		t.Fatalf("port %d never went away after interrupting sonar start", port)
	}
	waitForEmptyRunsList(t, ctx, c, 20*time.Second)
}

// interrupt asks a process to stop the way a terminal's Ctrl+C would.
func interrupt(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		// Windows has no SIGINT to send from another console; killing the
		// starter is what the Job Object turns into a tree kill.
		_ = cmd.Process.Kill()
		return
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupting sonar start: %v", err)
	}
}

// waitForAttributedPort is waitForAdded, but it hands back the row so the test
// can assert on the group and the run the resolver put on it.
//
// It waits for the delta that carries the run rather than the first delta that
// mentions the port. The two are usually the same one, but they need not be:
// the child binds its port before `sonar start` has had a chance to tell the
// daemon who owns it, and a scan tick landing in that window publishes a real
// listener that genuinely has no run yet. The daemon corrects it in the very
// next delta, which is the row this test is about.
func waitForAttributedPort(t *testing.T, sub *client.Subscription, port int, timeout time.Duration) (state.Port, bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case d, ok := <-sub.Deltas:
			if !ok {
				return state.Port{}, false
			}
			for _, rows := range [][]state.Port{d.Ports.Added, d.Ports.Updated} {
				for _, p := range rows {
					if p.Port != port {
						continue
					}
					if p.Run != nil {
						return p, true
					}
					t.Logf("delta seq %d has port %d before its run was registered (group %v)",
						d.Seq, port, p.Group)
				}
			}
		case <-deadline:
			return state.Port{}, false
		}
	}
}

// waitForEmptyRunsList polls until the registry has pruned the finished run.
func waitForEmptyRunsList(t *testing.T, ctx context.Context, c *client.Client, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var list rpc.RunsListResult
		if err := c.Call(ctx, "runs.list", rpc.Empty{}, &list); err != nil {
			t.Fatalf("runs.list: %v", err)
		}
		if len(list.Runs) == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("the run never left runs.list")
}

// safeBuffer is a strings.Builder that survives concurrent writes from a
// command's stdout and stderr.
type safeBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}
