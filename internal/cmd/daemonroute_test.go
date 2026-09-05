package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
)

// routedRows is the scan the in-process daemon serves. Nothing here is
// enriched, so what the daemon publishes and what the direct renderers would
// print from the same rows are comparable byte for byte.
func routedRows() []ports.ListeningPort {
	return []ports.ListeningPort{
		{
			Port: 3000, PID: 100, Process: "node", Command: "node server.js",
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
			User: "dev", Cwd: "/home/dev/web", Group: "web", GroupSource: "file",
			ProjectRoot: "/home/dev/web",
		},
		{
			Port: 5432, PID: 200, Process: "com.docker.backend",
			BindAddress: "0.0.0.0", IPVersion: "IPv4", Type: ports.PortTypeDocker,
			DockerContainer: "db-1", DockerImage: "postgres:17",
			DockerComposeService: "db", DockerComposeProject: "shop",
			DockerContainerPort: 5432,
		},
		{
			Port: 7000, PID: 300, Process: "Figma", IsApp: true,
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
		},
	}
}

// startTestDaemon runs a daemon over a real socket with a fixed scan result and
// points the CLI's dialer at it for the rest of the test.
func startTestDaemon(t *testing.T, rows []ports.ListeningPort) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}

	// Not t.TempDir(): a unix socket path is capped at ~104 bytes on macOS and
	// the test name would blow the budget.
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
				return append([]ports.ListeningPort{}, rows...), nil
			},
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-srv.Done()
	})
	if err := client.WaitForSocket(ctx, socket, 5*time.Second); err != nil {
		t.Fatalf("daemon did not come up: %v", err)
	}

	prev := dialDaemon
	dialDaemon = func(ctx context.Context) (*client.Client, error) {
		return client.Dial(ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	}
	t.Cleanup(func() { dialDaemon = prev })
}

// noDaemonReachable makes every connection attempt fail, the way an empty
// socket path does.
func noDaemonReachable(t *testing.T) {
	t.Helper()
	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) {
		return nil, errors.New("sonar daemon is not running")
	}
	t.Cleanup(func() { dialDaemon = prev })
}

// resetRouting clears the per-invocation state the flags and the once-only note
// keep, so tests do not leak into each other.
func resetRouting(t *testing.T) {
	t.Helper()
	prevFlag := noDaemonFlag
	noDaemonFlag = false
	fallbackNoteOnce = sync.Once{}
	t.Cleanup(func() {
		noDaemonFlag = prevFlag
		fallbackNoteOnce = sync.Once{}
	})
}

// captureStderr collects what fn writes to the real os.Stderr, which is where
// the fallback note goes.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stderr = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestListReadsThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	rows := routedRows()
	startTestDaemon(t, rows)

	got, index, err := listPorts(context.Background(), listQuery{})
	if err != nil {
		t.Fatalf("listPorts: %v", err)
	}
	if index != nil {
		t.Fatal("the daemon path must not build a local group index")
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (the desktop app is hidden)", len(got))
	}
	if got[0].Port != 3000 || got[1].Port != 5432 {
		t.Fatalf("got ports %d and %d, want 3000 and 5432", got[0].Port, got[1].Port)
	}
	if got[0].Group != "web" || got[0].GroupSource != "file" {
		t.Fatalf("the group did not survive the socket: %+v", got[0])
	}
	if got[1].DockerImage != "postgres:17" || got[1].DisplayName() != "db" {
		t.Fatalf("docker fields did not survive the socket: %+v", got[1])
	}
}

// TestDaemonAndDirectJSONMatch is spec integration test 6 at unit level: the
// rows the daemon publishes render to exactly the JSON a direct scan of the
// same ports renders to.
func TestDaemonAndDirectJSONMatch(t *testing.T) {
	resetRouting(t)
	rows := routedRows()
	startTestDaemon(t, rows)

	viaDaemon, _, err := listPorts(context.Background(), listQuery{})
	if err != nil {
		t.Fatalf("listPorts: %v", err)
	}
	direct := applyListFilters(append([]ports.ListeningPort{}, rows...), listQuery{})

	var daemonJSON, directJSON bytes.Buffer
	if err := display.RenderJSON(&daemonJSON, viaDaemon); err != nil {
		t.Fatalf("rendering daemon rows: %v", err)
	}
	if err := display.RenderJSON(&directJSON, direct); err != nil {
		t.Fatalf("rendering direct rows: %v", err)
	}
	if daemonJSON.String() != directJSON.String() {
		t.Fatalf("--json differs between the daemon and the direct path:\n daemon %s\n direct %s",
			daemonJSON.String(), directJSON.String())
	}
}

func TestListFiltersThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	startTestDaemon(t, routedRows())

	for _, tt := range []struct {
		name  string
		query listQuery
		want  []int
	}{
		{"apps hidden", listQuery{}, []int{3000, 5432}},
		{"apps shown", listQuery{showApps: true}, []int{3000, 5432, 7000}},
		{"by type", listQuery{filter: "docker"}, []int{5432}},
		{"by group", listQuery{group: "web"}, []int{3000}},
		{"by ip version", listQuery{ipVersion: "IPv6"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := listPorts(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("listPorts: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d rows, want %d", len(got), len(tt.want))
			}
			for i := range tt.want {
				if got[i].Port != tt.want[i] {
					t.Fatalf("row %d is port %d, want %d", i, got[i].Port, tt.want[i])
				}
			}
		})
	}
}

func TestFallbackNoteIsPrintedOncePerInvocation(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)

	out := captureStderr(t, func() {
		for range 3 {
			if c := daemonClient(context.Background()); c != nil {
				t.Error("daemonClient returned a client with no daemon reachable")
				c.Close()
			}
		}
	})
	if n := bytes.Count([]byte(out), []byte("note: daemon unavailable, using direct scan")); n != 1 {
		t.Fatalf("the fallback note appeared %d times, want exactly 1:\n%s", n, out)
	}
}

func TestNoDaemonFlagNeverDialsAndSaysNothing(t *testing.T) {
	resetRouting(t)
	noDaemonFlag = true

	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) {
		t.Error("--no-daemon still dialled the daemon")
		return nil, errors.New("unreachable")
	}
	t.Cleanup(func() { dialDaemon = prev })

	out := captureStderr(t, func() {
		if c := daemonClient(context.Background()); c != nil {
			t.Error("--no-daemon returned a client")
			c.Close()
		}
	})
	if out != "" {
		t.Fatalf("--no-daemon printed %q; it is a deliberate choice, not a fallback", out)
	}
}

func TestNextReadsThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	startTestDaemon(t, routedRows())

	got, err := nextFreePorts(context.Background(), 3000, 3005, 1)
	if err != nil {
		t.Fatalf("nextFreePorts: %v", err)
	}
	if len(got) != 1 || got[0] != 3001 {
		t.Fatalf("nextFreePorts = %v, want [3001]", got)
	}

	if _, err := nextFreePorts(context.Background(), 3000, 3000, 1); err == nil {
		t.Fatal("an exhausted range should report no free ports")
	}
}

func TestInspectReadsThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	startTestDaemon(t, routedRows())

	lp, err := inspectPort(context.Background(), 3000, "")
	if err != nil {
		t.Fatalf("inspectPort: %v", err)
	}
	if lp.PID != 100 || lp.DisplayName() == "" {
		t.Fatalf("inspect returned %+v", lp)
	}
	if lp.HealthStatus == "" {
		t.Fatal("inspect should carry a health probe result")
	}

	if _, err := inspectPort(context.Background(), 65000, ""); err == nil {
		t.Fatal("inspecting a port nothing listens on should fail")
	}
}

// listenerPort opens a real listener so ports.wait has something ready.
func listenerPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

// TestWaitExitCodes covers the three documented outcomes of `sonar wait`:
// 0 ready, 1 timeout, 2 interrupted.
func TestWaitExitCodes(t *testing.T) {
	resetRouting(t)
	startTestDaemon(t, routedRows())

	prevQuiet, prevTimeout, prevInterval := waitQuietFlag, waitTimeoutFlag, waitIntervalFlag
	waitQuietFlag = true
	waitIntervalFlag = 20 * time.Millisecond
	t.Cleanup(func() {
		waitQuietFlag, waitTimeoutFlag, waitIntervalFlag = prevQuiet, prevTimeout, prevInterval
	})

	ctx := context.Background()
	c, err := dialDaemon(ctx)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer c.Close()

	t.Run("ready", func(t *testing.T) {
		waitTimeoutFlag = 5 * time.Second
		code, err := waitThroughDaemon(ctx, c, []int{listenerPort(t)}, nil)
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		waitTimeoutFlag = 150 * time.Millisecond
		code, err := waitThroughDaemon(ctx, c, []int{1}, nil)
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		waitTimeoutFlag = 30 * time.Second
		sigCh := make(chan os.Signal, 1)
		sigCh <- os.Interrupt
		code, err := waitThroughDaemon(ctx, c, []int{1}, sigCh)
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	})
}

func TestGraphReadsThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	startTestDaemon(t, routedRows())

	// The fake rows own no real processes, so the graph is empty — what this
	// asserts is that the call is routed and decodes, not what lsof saw.
	conns, err := graphConnections(context.Background())
	if err != nil {
		t.Fatalf("graphConnections: %v", err)
	}
	if conns == nil {
		t.Fatal("graphConnections returned nil, want an empty slice")
	}
}

// TestListFallsBackToADirectScan checks the other half of the routing rule:
// with nothing listening on the socket, `sonar list` still works, and says so
// once on stderr.
func TestListFallsBackToADirectScan(t *testing.T) {
	resetRouting(t)
	noDaemonReachable(t)

	var rows []ports.ListeningPort
	var index *groups.Index
	var err error
	out := captureStderr(t, func() {
		rows, index, err = listPorts(context.Background(), listQuery{})
	})
	if err != nil {
		t.Skipf("the direct scan is not available here: %v", err)
	}
	if index == nil {
		t.Fatal("the direct path must resolve groups itself")
	}
	_ = rows
	if !strings.Contains(out, "note: daemon unavailable, using direct scan") {
		t.Fatalf("the fallback note is missing from stderr: %q", out)
	}
}
