//go:build !windows

package killer

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// This file exercises the killer against real processes: a shell that spawns a
// listener, which spawns a second listener of its own —
//
//	sh ── helper (port A) ── helper (port B)
//
// the shape of every dev server worth killing (`npm` → `vite` → its HMR
// socket). Killing port A with --tree must leave nothing behind on either port.

const (
	envHelperPort   = "SONAR_KILLER_HELPER_PORT"
	envHelperChild  = "SONAR_KILLER_HELPER_CHILD_PORT"
	envHelperIgnore = "SONAR_KILLER_HELPER_IGNORE_TERM"
)

// TestMain doubles as the helper process: re-executing the test binary with
// SONAR_KILLER_HELPER_PORT set turns it into a listener instead of a test run.
func TestMain(m *testing.M) {
	if port := os.Getenv(envHelperPort); port != "" {
		runHelper(port, os.Getenv(envHelperChild), os.Getenv(envHelperIgnore) != "")
		return
	}
	os.Exit(m.Run())
}

// runHelper listens on port, optionally spawns a child helper on childPort, and
// then blocks until it is signalled. With ignoreTerm it plays the wedged dev
// server that only SIGKILL can stop; otherwise the default SIGTERM disposition
// is what a well-behaved one gives us.
func runHelper(port, childPort string, ignoreTerm bool) {
	if ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(1)
	}
	defer ln.Close()

	if childPort != "" {
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), envHelperPort+"="+childPort, envHelperChild+"=")
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// freePort asks the kernel for a port nothing is using and hands it back.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func portAnswers(port int) bool { return probePort(port, "127.0.0.1") }

func waitForPort(t *testing.T, port int, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if portAnswers(port) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %d: still open=%v after 10s, wanted open=%v", port, !want, want)
}

// startTree launches `sh -c '<test binary> & wait'` so that the listener really
// is a child of a shell, and returns the two ports it serves.
func startTree(t *testing.T) (parentPort, childPort int, shell *exec.Cmd) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	parentPort, childPort = freePort(t), freePort(t)

	shell = exec.Command("sh", "-c", `"$0" & wait`, exe)
	shell.Env = append(os.Environ(),
		envHelperPort+"="+strconv.Itoa(parentPort),
		envHelperChild+"="+strconv.Itoa(childPort),
	)
	if err := shell.Start(); err != nil {
		t.Fatalf("starting the helper tree: %v", err)
	}
	t.Cleanup(func() {
		// Whatever the test did, leave no strays behind.
		if shell.Process != nil {
			_ = syscallKillTree(shell.Process.Pid)
		}
		_ = shell.Wait()
	})

	waitForPort(t, parentPort, true)
	waitForPort(t, childPort, true)
	return parentPort, childPort, shell
}

// syscallKillTree is the test's own belt-and-braces cleanup.
func syscallKillTree(pid int) error {
	table := scanProcessTable()
	for _, p := range table.Descendants(pid) {
		_ = signalProcess(p, true)
	}
	return nil
}

// snapshotFor returns the scanner's view of a port, falling back to a
// hand-built row when the scan cannot see it (an unprivileged sandbox without
// lsof still exercises everything that matters: the ps table, the signals and
// the port probe).
func snapshotFor(t *testing.T, port int) []ports.ListeningPort {
	t.Helper()
	if found, err := ports.Scan(); err == nil {
		docker.EnrichPorts(found)
		ports.Enrich(found)
		for i := range found {
			if found[i].Port == port {
				return found
			}
		}
	}
	pid := ownerOf(t, port)
	return []ports.ListeningPort{{
		Port: port, PID: pid, BindAddress: "127.0.0.1",
		Process: "killer.test", Type: ports.PortTypeUser,
	}}
}

// ownerOf finds the helper process listening on port in the ps table, by
// matching the environment the shell handed it. Used only by the fallback path.
func ownerOf(t *testing.T, port int) int {
	t.Helper()
	table := scanProcessTable()
	exe, _ := os.Executable()
	for pid, p := range table {
		if p.Command == exe && pid != os.Getpid() {
			// Two helpers share the command line; the one that answers on
			// this port is the one whose descendants include the other.
			if len(table.Descendants(pid)) > 1 {
				return pid
			}
		}
	}
	t.Fatalf("could not find the process listening on port %d", port)
	return 0
}

func TestKillPortsStopsARealProcessTree(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	parentPort, childPort, shell := startTree(t)

	results := KillPorts(context.Background(),
		[]Target{{Port: parentPort}},
		Options{Tree: true, Grace: 2 * time.Second, Ports: snapshotFor(t, parentPort)},
	)

	if len(results) == 0 {
		t.Fatal("no result rows")
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("row %+v failed: %s", r, r.Error)
		}
	}
	// The listener and the port it spawned are both gone.
	waitForPort(t, parentPort, false)
	waitForPort(t, childPort, false)

	// And the shell that was waiting on them has nothing left to wait for.
	done := make(chan struct{})
	go func() { _ = shell.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell is still running after its tree was killed")
	}
}

func TestKillPortsDryRunLeavesARealTreeRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	parentPort, childPort, _ := startTree(t)

	results := KillPorts(context.Background(),
		[]Target{{Port: parentPort}},
		Options{Tree: true, DryRun: true, Ports: snapshotFor(t, parentPort)},
	)

	if len(results) < 2 {
		t.Fatalf("dry run planned %d rows, want the listener and its child", len(results))
	}
	if !portAnswers(parentPort) || !portAnswers(childPort) {
		t.Fatal("dry run killed something")
	}
	// The child is planned before the parent.
	last := results[len(results)-1]
	for _, r := range results[:len(results)-1] {
		if !contains(scanProcessTable().Descendants(last.PID), r.PID) {
			t.Errorf("row %+v is not below the last row (PID %d)", r, last.PID)
		}
	}
}

func contains(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func TestKillPortsEscalatesOnAProcessThatIgnoresSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	port := freePort(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wedged := exec.Command(exe)
	wedged.Env = append(os.Environ(),
		envHelperPort+"="+strconv.Itoa(port),
		envHelperIgnore+"=1",
	)
	if err := wedged.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wedged.Process != nil {
			_ = signalProcess(wedged.Process.Pid, true)
		}
		_ = wedged.Wait()
	})
	waitForPort(t, port, true)

	// The listener ignores SIGTERM, so only the escalation to SIGKILL frees
	// the port. A short grace keeps the test quick.
	snapshot := []ports.ListeningPort{{
		Port: port, PID: wedged.Process.Pid, BindAddress: "127.0.0.1",
		Process: "killer.test", Type: ports.PortTypeUser,
	}}
	results := KillPorts(context.Background(), []Target{{Port: port}},
		Options{Grace: 500 * time.Millisecond, Ports: snapshot})

	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Method != state.MethodSIGKILL {
		t.Fatalf("method = %q, want sigkill after the grace period", results[0].Method)
	}
	waitForPort(t, port, false)
}
