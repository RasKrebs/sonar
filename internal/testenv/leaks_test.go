package testenv

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The bug this pins: the gate used to match any `sonar serve` running from
// anywhere under the machine's temp directory, so a run killed the daemons of
// every other worktree that was building into a temp directory of its own at
// the same moment, and failed those runs with a leak they had not caused.
func TestGateMatchesOnlyThisRunsRoot(t *testing.T) {
	skipWithoutPS(t)

	// The space in the directory name is the `ps` parsing case: ps quotes
	// nothing, so a gate that split the command line on whitespace would read
	// this process as the two arguments "<root>/gate" and "inside/sonar".
	inside := startFakeDaemon(t, filepath.Join(Root(), "gate inside"))
	outsideRoot, err := os.MkdirTemp(machineTempDir(), "sonar-gate-outside")
	if err != nil {
		t.Fatalf("creating a directory outside this run's root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(outsideRoot) })
	outside := startFakeDaemon(t, outsideRoot)

	t.Setenv(gateAllEnv, "")
	leaked := LeakedDaemons()
	if !hasPID(leaked, inside) {
		t.Errorf("the gate missed pid %d, a `serve` under this run's root %s: %v", inside, Root(), leaked)
	}
	if hasPID(leaked, outside) {
		t.Errorf("the gate claimed pid %d, a `sonar serve` outside this run's root %s: %v", outside, Root(), leaked)
	}

	// CI is one runner with one suite, so it opts back into the wide net.
	t.Setenv(gateAllEnv, "1")
	if leaked := LeakedDaemons(); !hasPID(leaked, outside) {
		t.Errorf("%s=1 did not restore the machine-wide match for pid %d: %v", gateAllEnv, outside, leaked)
	}
}

// Every child of the test process writes its temp files into this run's root,
// which is what makes the gate's ownership rule true of the binaries the
// integration harnesses build with `go build -o`.
func TestChildrenInheritTheRunRootAsTMPDIR(t *testing.T) {
	if !sameDir(os.TempDir(), Root()) {
		t.Fatalf("os.TempDir() = %s, want this run's root %s", os.TempDir(), Root())
	}
	RequireIsolated(t, t.TempDir())

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), helperEnv+"="+helperTempDir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("asking a child process for its temp directory: %v", err)
	}
	if !sameDir(string(out), Root()) {
		t.Errorf("a child process resolved its temp directory to %s, want %s", out, Root())
	}
}

// startFakeDaemon runs a process that looks to `ps` exactly like a leaked
// daemon — a binary named sonar in dir, with `serve` as its first argument —
// and returns its pid. It is this test binary under another name, told by the
// environment to sit still rather than run the suite.
func startFakeDaemon(t *testing.T, dir string) int {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	bin := filepath.Join(dir, "sonar")
	if err := clone(self, bin); err != nil {
		t.Fatalf("putting a stand-in binary at %s: %v", bin, err)
	}

	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), helperEnv+"="+helperServe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("opening the stand-in's stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the stand-in daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// clone puts a runnable copy of src at dst, by link where the filesystem
// allows it because the test binary is not small.
func clone(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func hasPID(daemons []Daemon, pid int) bool {
	for _, d := range daemons {
		if d.PID == pid {
			return true
		}
	}
	return false
}

func skipWithoutPS(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the leak gate reads ps, which Windows does not have")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skipf("no ps on this machine: %v", err)
	}
}
