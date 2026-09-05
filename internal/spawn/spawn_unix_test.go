//go:build !windows

package spawn

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether a pid still exists (signal 0 is the portable probe).
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// waitGone polls until pid is gone, or fails the test.
func waitGone(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s (pid %d) is still running", what, pid)
}

// waitFile polls until path exists and is non-empty, then returns its content.
func waitFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %s", path, timeout)
	return ""
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
	// `npm run dev`: killing the child alone would orphan the listener.
	body := "#!/bin/sh\n" +
		"sh -c 'echo $$ > " + pidFile + "; exec sleep 300'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	// Files, not pipes: a surviving grandchild would hold a pipe open and hide
	// the very failure this test is looking for.
	out, err := os.Create(filepath.Join(dir, "out.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	h, err := Spawn(context.Background(), Request{
		Argv: []string{script}, Cwd: dir, Group: "itest", Name: "dev.sh",
		Stdout: out, Stderr: out,
	})
	if err != nil {
		t.Fatal(err)
	}

	grandchild, err := strconv.Atoi(strings.TrimSpace(waitFile(t, pidFile, 5*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if !alive(grandchild) {
		t.Fatalf("the grandchild (pid %d) never started", grandchild)
	}

	// The child is in its own process group, so the terminal's SIGINT would
	// never reach it: ForwardSignals is what makes Ctrl+C work.
	stop := h.ForwardSignals()
	defer stop()
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
	case <-time.After(5 * time.Second):
		_ = h.Kill()
		t.Fatal("the child did not exit after a forwarded SIGINT")
	}
	waitGone(t, "the grandchild", grandchild)
}

// TestDetachedRunSurvivesItsStarter re-execs this test binary as the starter so
// the child really is orphaned when the process that spawned it goes away.
func TestDetachedRunSurvivesItsStarter(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	pidFile := filepath.Join(dir, "pid")

	starter := exec.Command(os.Args[0], "-test.run=TestSpawnDetachedHelper", "-test.v")
	starter.Env = append(os.Environ(),
		"SONAR_SPAWN_HELPER=1",
		"SONAR_LOG_DIR="+logDir,
		"SONAR_SPAWN_PIDFILE="+pidFile,
	)
	if out, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("starter failed: %v: %s", err, out)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(waitFile(t, pidFile, 5*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	if !alive(pid) {
		t.Fatalf("the detached run (pid %d) died with its starter", pid)
	}
	logPath := filepath.Join(logDir, "itest", "sleeper.log")
	if got := waitFile(t, logPath, 5*time.Second); !strings.Contains(got, "detached-hello") {
		t.Fatalf("%s = %q, want the child's output", logPath, got)
	}
}

// TestSpawnDetachedHelper is not a test: it is the starter process for
// TestDetachedRunSurvivesItsStarter, and does nothing in a normal run.
func TestSpawnDetachedHelper(t *testing.T) {
	if os.Getenv("SONAR_SPAWN_HELPER") == "" {
		t.Skip("helper process for TestDetachedRunSurvivesItsStarter")
	}
	h, err := Spawn(context.Background(), Request{
		Argv:   []string{"sh", "-c", "echo detached-hello; sleep 30"},
		Cwd:    t.TempDir(),
		Group:  "itest",
		Name:   "sleeper",
		Detach: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("SONAR_SPAWN_PIDFILE"),
		[]byte(strconv.Itoa(h.PID)), 0o644); err != nil {
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
