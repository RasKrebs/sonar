package cmd

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/spf13/cobra"
)

// TestKillHelperListener is not a test: re-executing the test binary with
// SONAR_TEST_LISTEN set is how these tests get a real, killable process holding
// a real port.
func TestKillHelperListener(t *testing.T) {
	port := os.Getenv("SONAR_TEST_LISTEN")
	if port == "" {
		t.Skip("helper process, driven by TestKillRoutesThroughTheDaemon")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("helper could not listen: %v", err)
	}
	defer ln.Close()
	time.Sleep(60 * time.Second)
}

// startListener spawns the helper on a free port and returns the port. The
// process is killed on cleanup whether or not the test killed it itself.
func startListener(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the kill routing harness is unix-only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)

	cmd := exec.Command(os.Args[0], "-test.run=TestKillHelperListener", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "SONAR_TEST_LISTEN="+portStr)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+portStr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the helper never listened on %d", port)
	return 0
}

// startScanningDaemon runs a daemon over a real socket with the production
// scanner, so it sees the same processes the direct path scans.
func startScanningDaemon(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	dir, err := os.MkdirTemp("", "sn")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		Scanner: scanner.New(scanner.Options{DaemonVersion: "test"}),
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

// captureStdout collects what fn prints, which is where every kill report goes.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stdout = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

// setKillFlags installs one flag combination for the duration of a test.
func setKillFlags(t *testing.T, dryRun, asJSON bool) {
	t.Helper()
	prevDry, prevJSON, prevYes := killDryRunFlag, killJSONFlag, killYesFlag
	killDryRunFlag, killJSONFlag, killYesFlag = dryRun, asJSON, true
	t.Cleanup(func() {
		killDryRunFlag, killJSONFlag, killYesFlag = prevDry, prevJSON, prevYes
	})
}

// runKillCmd drives the real command, so the routing decision itself — daemon
// when one is reachable, direct scan otherwise — is what the test exercises.
func runKillCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{RunE: runKill}
	cmd.Flags().String("ip", "", "")
	cmd.SetContext(context.Background())
	var err error
	out := captureStdout(t, func() { err = runKill(cmd, args) })
	return out, err
}

// TestKillJSONIsIdenticalOnBothPaths is the golden for contract §22's third
// follow-up: routing `sonar kill` through the daemon must not change a single
// byte of what the user sees. A dry run is what makes the comparison possible —
// the same process is planned twice, so pid, name and method are identical and
// the two JSON documents have to match exactly.
func TestKillJSONIsIdenticalOnBothPaths(t *testing.T) {
	resetRouting(t)
	setKillFlags(t, true, true)
	port := startListener(t)
	arg := []string{strconv.Itoa(port)}

	noDaemonReachable(t)
	direct, err := runKillCmd(t, arg...)
	if err != nil {
		t.Fatalf("the direct path failed: %v", err)
	}
	if !bytes.Contains([]byte(direct), []byte(`"port": `+arg[0])) {
		t.Fatalf("the direct path did not find the listener:\n%s", direct)
	}

	resetRouting(t)
	startScanningDaemon(t)
	routed, err := runKillCmd(t, arg...)
	if err != nil {
		t.Fatalf("the daemon path failed: %v", err)
	}

	if routed != direct {
		t.Fatalf("--json differs between the two paths:\n daemon:\n%s\n direct:\n%s", routed, direct)
	}
}

// TestKillRoutesThroughTheDaemon: with a daemon reachable the kill happens in
// the daemon, which rescans straight afterwards — so the port is gone from the
// daemon's own port table immediately, instead of lingering in its cache for up
// to CacheTTL as it did when the CLI killed directly.
func TestKillRoutesThroughTheDaemon(t *testing.T) {
	resetRouting(t)
	setKillFlags(t, false, true)
	port := startListener(t)
	startScanningDaemon(t)

	out, err := runKillCmd(t, strconv.Itoa(port))
	if err != nil {
		t.Fatalf("kill through the daemon: %v\n%s", err, out)
	}
	if !bytes.Contains([]byte(out), []byte(`"ok": true`)) {
		t.Fatalf("nothing was stopped:\n%s", out)
	}

	ctx := context.Background()
	c, err := dialDaemon(ctx)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer c.Close()

	// The daemon's own view is already up to date: no sleeping out the cache.
	rows, err := daemonList(ctx, c, rpc.PortsListParams{All: true})
	if err != nil {
		t.Fatalf("ports.list: %v", err)
	}
	for _, r := range rows {
		if r.Port == port {
			t.Fatalf("port %d is still in the daemon's table right after the kill", port)
		}
	}
}
