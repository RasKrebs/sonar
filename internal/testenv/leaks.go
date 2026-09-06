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
//
// What it may look at is bounded by ownership. The gate kills what it finds,
// so a rule that matched more than this run started would make one suite kill
// another's daemon and fail that run for a leak it did not cause — which is
// what "any sonar serve under the temp directory" did on a machine running
// several worktrees at once. Everything this run starts is under its own temp
// root (see testenv.Root), and that is the whole of the search space unless
// SONAR_TESTENV_GATE_ALL=1 says the machine belongs to one suite.

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
// stopped: this test binary running as `serve`, and anything running `serve`
// from an executable under this run's own temp root (Root) — which is where
// the integration harnesses build the real binary they drive. Nothing else is
// ever a candidate, so neither a developer's installed daemon nor another
// `go test` sharing the machine can be mistaken for this run's leak.
//
// SONAR_TESTENV_GATE_ALL=1 widens the second rule back to any `sonar serve`
// running from anywhere under the machine's temp directory. That is right on a
// CI runner, which has one checkout and one suite and would rather over-match
// than miss a stray daemon, and wrong on a developer's machine, where several
// worktrees run their suites at once.
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
	gateAll := os.Getenv(gateAllEnv) == "1"
	var leaked []Daemon
	for _, line := range strings.Split(string(out), "\n") {
		pid, cmdline, ok := splitPS(line)
		if !ok || pid == self {
			continue
		}
		if isDaemonOf(exe, cmdline) || isRunRootDaemon(cmdline) || (gateAll && isTempBuiltDaemon(cmdline)) {
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
	path, ok := splitServe(cmdline)
	return ok && path == exe
}

// isRunRootDaemon reports whether cmdline is a `serve` whose executable lives
// under this run's temp root. Only this run puts anything there, so the name
// of the binary does not have to be checked and another run's daemon can never
// match.
func isRunRootDaemon(cmdline string) bool {
	path, ok := splitServe(cmdline)
	return ok && runRoot != "" && under(path, runRoot)
}

// isTempBuiltDaemon reports whether cmdline is a `sonar serve` running from a
// binary anywhere under the machine's temp directory. It is the opt-in
// machine-wide net, and it deliberately catches other runs' daemons too.
func isTempBuiltDaemon(cmdline string) bool {
	path, ok := splitServe(cmdline)
	if !ok {
		return false
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	return name == "sonar" && under(path, machineTempDir())
}

// machineTempDir is the temp directory the machine had before Isolate pointed
// TMPDIR at this run's root.
func machineTempDir() string {
	if realTemp != "" {
		return realTemp
	}
	return os.TempDir()
}

// splitServe returns the executable path of a `ps` command line whose first
// argument is `serve`, and reports whether it had that shape.
//
// `ps -axo command=` joins argv with single spaces and quotes nothing, so a
// path containing spaces cannot be recovered by splitting on whitespace: an
// executable at "/tmp/my dir/sonar" would look like the two arguments
// "/tmp/my" and "dir/sonar". What can be recovered is the boundary — the
// executable is everything before the first " serve" that stands as a whole
// argument rather than as part of a longer one.
func splitServe(cmdline string) (string, bool) {
	const arg = " serve"
	for i := 0; i < len(cmdline); {
		j := strings.Index(cmdline[i:], arg)
		if j < 0 {
			return "", false
		}
		j += i
		if end := j + len(arg); end == len(cmdline) || cmdline[end] == ' ' {
			return cmdline[:j], true
		}
		i = j + 1
	}
	return "", false
}
