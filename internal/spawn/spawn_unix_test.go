//go:build !windows

package spawn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Every wait in this file is a poll with the same generous bound. These tests
// fork real processes and run next to the rest of the suite, so a deadline
// tight enough to feel fast is only a bet that the machine is idle: on a loaded
// CI runner a fork, an exec and a first write can easily take seconds. A
// correct run reaches the condition long before the deadline and never spends
// it, so the cost of being generous is paid only by a genuine failure.
const (
	waitTimeout  = 30 * time.Second
	waitInterval = 50 * time.Millisecond
)

// alive reports whether a pid still exists (signal 0 is the portable probe).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// waitUntil polls cond until it holds or waitTimeout passes. cond is always
// evaluated once more when the deadline expires, so a condition that becomes
// true during the last sleep is not reported as a timeout.
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(waitTimeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return cond()
		}
		time.Sleep(waitInterval)
	}
}

// waitGone polls until pid is gone, or fails the test.
func waitGone(t *testing.T, what string, pid int) {
	t.Helper()
	if !waitUntil(func() bool { return !alive(pid) }) {
		t.Fatalf("%s (pid %d) is still running after %s", what, pid, waitTimeout)
	}
}

// waitFile polls until path holds something other than whitespace and returns
// it. Used for readiness files that are published atomically (written to a
// temporary name and renamed), so any content at all is the whole content.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	var last string
	if !waitUntil(func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		last = string(b)
		return len(bytes.TrimSpace(b)) > 0
	}) {
		t.Fatalf("%s did not appear within %s (last read %q)", path, waitTimeout, last)
	}
	return last
}

// waitFileContains polls until path contains want.
//
// A single read is never enough for a file a child process is still writing:
// the read can land between the open and the first write, in the middle of a
// line, or — on a host where /bin/sh is dash — after a warning the shell
// printed ahead of the output under test. Waiting for the content that matters
// is the only check that means what it says.
func waitFileContains(t *testing.T, path, want string) string {
	t.Helper()
	var last string
	if !waitUntil(func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		last = string(b)
		return strings.Contains(last, want)
	}) {
		t.Fatalf("%s never contained %q within %s (last read %q)", path, want, waitTimeout, last)
	}
	return last
}

// waitPID is waitFile plus the parse every caller here does.
func waitPID(t *testing.T, path string) int {
	t.Helper()
	raw := strings.TrimSpace(waitFile(t, path))
	pid, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s = %q, want a pid: %v", path, raw, err)
	}
	return pid
}

// publishPID is the shell fragment a test helper uses to announce its own pid:
// write to a temporary name, then rename. A reader therefore sees either no
// file or the complete pid, never the empty file a plain `>` redirection leaves
// behind between the open and the write. The destination is $0, which `sh -c
// SCRIPT NAME` sets to NAME — that keeps the path out of the quoted script.
//
// It is POSIX sh and nothing more, because /bin/sh is dash on Debian and
// Ubuntu: no bashisms, and every expansion of $0 is quoted.
const publishPID = `echo $$ > "$0.tmp" && mv "$0.tmp" "$0"`

// shQuote wraps s for a POSIX sh single-quoted word.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestSpawnInjectsTheRunEnvironment(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	h, err := Spawn(context.Background(), Request{
		Argv:     []string{"sh", "-c", `echo "$SONAR_GROUP/$SONAR_NAME/$SONAR_RUN_ID/$SONAR_PORT"`},
		Cwd:      dir,
		Group:    "itest",
		Name:     "web",
		PortHint: 8123,
		ID:       "run-1",
		Stdout:   &out,
		Stderr:   &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := h.Wait()
	if err != nil || code != 0 {
		t.Fatalf("child exited %d: %v (%s)", code, err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "itest/web/run-1/8123" {
		t.Fatalf("child environment = %q", got)
	}
}

func TestSpawnPropagatesTheExitCode(t *testing.T) {
	h, err := Spawn(context.Background(), Request{
		Argv:   []string{"sh", "-c", "exit 7"},
		Cwd:    t.TempDir(),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := h.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

// TestForwardedInterruptKillsTheWholeProcessGroup is the acceptance behaviour
// of `sonar start`: Ctrl+C on the starter takes down the command *and* the
// listener it spawned, because the child runs in its own process group.
func TestForwardedInterruptKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	script := filepath.Join(dir, "dev.sh")
	// A dev.sh whose real work happens in a grandchild, the shape of every
	// `npm run dev`: killing the child alone would orphan the listener. The
	// grandchild publishes its pid before it does anything else, so the test's
	// wait is bounded by one fork and not by whatever the helper does next.
	body := "#!/bin/sh\n" +
		"sh -c '" + publishPID + "; exec sleep 300' " + shQuote(pidFile) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	// Files, not pipes: a surviving grandchild would hold a pipe open and hide
	// the very failure this test is looking for.
	logPath := filepath.Join(dir, "out.log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	// The child is in its own process group, so the terminal's SIGINT would
	// never reach it: the forwarder is what makes Ctrl+C work, and it is
	// installed before the child so the child starts with a default SIGINT.
	fwd := CatchSignals()
	defer fwd.Stop()

	h, err := Spawn(context.Background(), Request{
		Argv: []string{script}, Cwd: dir, Group: "itest", Name: "dev.sh",
		Stdout: out, Stderr: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	fwd.Forward(h)
	// Only on a failure: a passing run has already reaped both processes, and
	// signalling a reaped pid risks hitting whatever the OS recycled it into.
	t.Cleanup(func() {
		if t.Failed() {
			_ = h.Kill()
		}
	})

	grandchild := waitPID(t, pidFile)
	if !alive(grandchild) {
		t.Fatalf("the grandchild (pid %d) never started", grandchild)
	}
	t.Cleanup(func() {
		if t.Failed() {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
		}
	})

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() { code, _ := h.Wait(); done <- code }()
	select {
	case code := <-done:
		if code != 130 {
			t.Errorf("exit code = %d, want 130 (128+SIGINT)", code)
		}
	case <-time.After(waitTimeout):
		_ = h.Kill()
		b, _ := os.ReadFile(logPath)
		t.Fatalf("the child did not exit after a forwarded SIGINT within %s (log %q)", waitTimeout, b)
	}
	waitGone(t, "the grandchild", grandchild)
}

// TestDetachedRunSurvivesItsStarter re-execs this test binary as the starter so
// the child really is orphaned when the process that spawned it goes away.
func TestDetachedRunSurvivesItsStarter(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	pidFile := filepath.Join(dir, "pid")
	// The detached child's working directory has to outlive the starter, so it
	// is this test's temp dir and not one the starter cleans up on its way out.
	// A child left standing in a deleted directory is not merely untidy: dash
	// greets it with `sh: 0: getcwd() failed` on the run's own log, ahead of
	// anything the command prints.
	childCwd := filepath.Join(dir, "cwd")
	if err := os.MkdirAll(childCwd, 0o755); err != nil {
		t.Fatal(err)
	}

	starter := exec.Command(os.Args[0], "-test.run=TestSpawnDetachedHelper", "-test.v")
	starter.Env = append(os.Environ(),
		"SONAR_SPAWN_HELPER=1",
		"SONAR_LOG_DIR="+logDir,
		"SONAR_SPAWN_PIDFILE="+pidFile,
		"SONAR_SPAWN_CWD="+childCwd,
	)
	if out, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("starter failed: %v: %s", err, out)
	}

	// The starter only writes the pid file once the child's output is on disk,
	// so by here the log is readable — but it is still polled, because the
	// starter is not the only writer this test does not synchronise with.
	pid := waitPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	if !alive(pid) {
		t.Fatalf("the detached run (pid %d) died with its starter", pid)
	}
	waitFileContains(t, filepath.Join(logDir, "itest", "sleeper.log"), detachedMarker)
}

// detachedMarker is the line the detached run prints, and the readiness signal
// the starter waits for before it publishes the run's pid.
const detachedMarker = "detached-hello"

// TestSpawnDetachedHelper is not a test: it is the starter process for
// TestDetachedRunSurvivesItsStarter, and does nothing in a normal run.
func TestSpawnDetachedHelper(t *testing.T) {
	if os.Getenv("SONAR_SPAWN_HELPER") == "" {
		t.Skip("helper process for TestDetachedRunSurvivesItsStarter")
	}
	// `printf` rather than `echo`, and a marker with no escapes in it, so the
	// line is byte-for-byte the same under every /bin/sh.
	h, err := Spawn(context.Background(), Request{
		Argv:   []string{"sh", "-c", fmt.Sprintf("printf '%%s\\n' %s; exec sleep 300", shQuote(detachedMarker))},
		Cwd:    os.Getenv("SONAR_SPAWN_CWD"),
		Group:  "itest",
		Name:   "sleeper",
		Detach: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Readiness is the output itself: the pid file is published only once the
	// marker has reached the log, so the parent never races the child's first
	// write. Waiting on the log also flushes out the shell's own start-up
	// noise, which lands there first when it lands at all.
	waitFileContains(t, h.LogPath, detachedMarker)

	pidFile := os.Getenv("SONAR_SPAWN_PIDFILE")
	if err := os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(h.PID)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pidFile+".tmp", pidFile); err != nil {
		t.Fatal(err)
	}
}

func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONAR_LOG_DIR", dir)
	path := LogPath("group/with/slash", "name")
	if want := filepath.Join(dir, "group-with-slash", "name.log"); path != want {
		t.Fatalf("LogPath = %q, want %q", path, want)
	}

	f, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("x"), int(LogMaxBytes)+1)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Reopening past the limit rotates, twice over, and keeps three generations.
	for i := 0; i < 2; i++ {
		f, err := OpenLog(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(bytes.Repeat([]byte("y"), int(LogMaxBytes)+1)); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	for _, name := range []string{"name.log", "name.log.1", "name.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, "group-with-slash", name)); err != nil {
			t.Errorf("missing generation %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "group-with-slash", "name.log.3")); err == nil {
		t.Error("name.log.3 should have been dropped")
	}
}
