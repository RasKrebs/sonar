package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The leak gate.
//
// A test that reaches the CLI's autostart path spawns "<this binary> serve
// --detach". The guards in internal/daemon and internal/daemon/client stop
// that spawn from happening at all, but a guard can be bypassed (a harness
// sets SONAR_ALLOW_TEST_DAEMON, a new code path forgets to consult it), and a
// leak is silent: the parent test passes and the stray process lives on. So
// every isolated package also *looks*, after its tests have run, and fails the
// run if anything is still there.

// Daemon is one stray process left behind by this test binary.
type Daemon struct {
	PID     int
	Command string
}

func (d Daemon) String() string { return fmt.Sprintf("pid %d: %s", d.PID, d.Command) }

// AssertNoLeakedDaemons reports every process this test binary left running as
// a daemon, kills them, and prints what it found. It returns the leaks so
// TestMain can turn a non-empty result into a failing exit code; Run already
// does that.
//
// Windows has no ps and no cheap way to read another process's command line
// from Go, so the check is unix-only. The guards it backs up are not.
func AssertNoLeakedDaemons() []Daemon {
	// A cleanup that has asked a daemon to stop is racing us: the socket is
	// already gone but the process has not been reaped. Give teardown a moment
	// before calling it a leak, so the gate catches real leaks and not slow
	// exits.
	var leaked []Daemon
	for deadline := time.Now().Add(2 * time.Second); ; {
		if leaked = LeakedDaemons(); len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(leaked) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "\ntestenv: %d leaked daemon process(es) survived this test binary:\n", len(leaked))
	for _, d := range leaked {
		fmt.Fprintf(os.Stderr, "  %s\n", d)
		if p, err := os.FindProcess(d.PID); err == nil {
			_ = p.Kill()
		}
	}
	fmt.Fprintf(os.Stderr, "testenv: they were killed, but a test started a daemon it did not stop.\n")
	return leaked
}

// RequireNoLeakedDaemons is AssertNoLeakedDaemons for a single test.
func RequireNoLeakedDaemons(t testing.TB) {
	t.Helper()
	if leaked := AssertNoLeakedDaemons(); len(leaked) > 0 {
		t.Fatalf("the test left %d daemon process(es) running: %v", len(leaked), leaked)
	}
}

// LeakedDaemons lists running processes that this test run should have
// stopped: this test binary running as `serve`, and any `sonar serve` started
// from a binary the tests built into a temp directory. Nothing installed on
// the machine matches either shape, so a developer's own daemon is never
// mistaken for a leak.
func LeakedDaemons() []Daemon {
	if runtime.GOOS == "windows" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		// No ps (a minimal container): the gate is best-effort, and the
		// guards are what actually prevent the leak.
		return nil
	}

	self := os.Getpid()
	var leaked []Daemon
	for _, line := range strings.Split(string(out), "\n") {
		pid, cmdline, ok := splitPS(line)
		if !ok || pid == self {
			continue
		}
		if isDaemonOf(exe, cmdline) || isTempBuiltDaemon(cmdline) {
			leaked = append(leaked, Daemon{PID: pid, Command: cmdline})
		}
	}
	return leaked
}

// splitPS pulls the pid and the command line out of one `ps -axo pid=,command=`
// row.
func splitPS(line string) (int, string, bool) {
	line = strings.TrimSpace(line)
	space := strings.IndexByte(line, ' ')
	if space < 0 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(line[:space])
	if err != nil {
		return 0, "", false
	}
	return pid, strings.TrimSpace(line[space+1:]), true
}

// isDaemonOf reports whether cmdline is exe running as a daemon. `serve` is
// the only argument that makes a sonar process a daemon, and it is always the
// first one.
//
// The match is on the full path and nothing looser. `go test ./...` runs
// packages in parallel and names every test binary <pkg>.test, so matching on
// the file name would let one package's gate fail on another package's
// process — which is a flaky gate, and a flaky gate gets deleted.
func isDaemonOf(exe, cmdline string) bool {
	rest, ok := strings.CutPrefix(cmdline, exe+" ")
	if !ok {
		return false
	}
	fields := strings.Fields(rest)
	return len(fields) > 0 && fields[0] == "serve"
}

// isTempBuiltDaemon reports whether cmdline is a `sonar serve` running from a
// binary under the temp directory — which only the integration harnesses, who
// build one there, ever produce.
func isTempBuiltDaemon(cmdline string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) < 2 || fields[1] != "serve" {
		return false
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(fields[0])), ".exe")
	return name == "sonar" && under(fields[0], os.TempDir())
}
