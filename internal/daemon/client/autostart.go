package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
)

// pollInterval is how often Autostart retries the dial while waiting.
const pollInterval = 25 * time.Millisecond

// NoAutostartEnv disables autostart for a whole process tree. `sonar` never
// starts a daemon behind the back of anything that sets it, which is what the
// test harness (internal/testenv) and CI use to keep a build from leaving a
// daemon running on the machine.
const NoAutostartEnv = "SONAR_NO_AUTOSTART"

// Autostart spawns `sonar serve --detach` and waits, up to AutostartTimeout,
// for the socket to accept connections (contract §7). binary defaults to the
// running executable, so a client always starts the daemon it was built with.
//
// It refuses in two cases, both of them "this is not a sonar install": when
// NoAutostartEnv is set, and when the executable it would spawn is a Go test
// binary (see daemon.IsTestBinary — that spawn re-runs the test suite in the
// background rather than starting a daemon).
func Autostart(ctx context.Context, binary, socket string) error {
	if daemon.EnvEnabled(os.Getenv(NoAutostartEnv)) {
		return fmt.Errorf("%w: autostart is disabled by %s=%s",
			ErrNotRunning, NoAutostartEnv, os.Getenv(NoAutostartEnv))
	}

	exe, err := resolveBinary(binary)
	if err != nil {
		return err
	}
	if daemon.IsTestBinary(exe) && !daemon.TestDaemonAllowed() {
		return fmt.Errorf("%w: refusing to autostart %s: it is a Go test binary, and `%s serve --detach` re-runs the whole test suite in the background instead of starting a daemon\nhint: a test needs an in-process daemon, or %s=1 to override",
			ErrNotRunning, filepath.Base(exe), filepath.Base(exe), daemon.AllowTestDaemonEnv)
	}

	cmd := exec.Command(exe, "serve", "--detach")
	cmd.Env = os.Environ()
	if socket != "" {
		cmd.Env = append(cmd.Env, "SONAR_SOCKET="+socket)
	}
	// `serve --detach` forks the real daemon and exits, so this wait is short.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: starting `%s serve --detach` failed: %v: %s",
			ErrNotRunning, filepath.Base(exe), err, trim(out))
	}

	return WaitForSocket(ctx, socket, AutostartTimeout)
}

// WaitForSocket blocks until the daemon accepts connections at socket, or the
// timeout expires.
func WaitForSocket(ctx context.Context, socket string, timeout time.Duration) error {
	if socket == "" {
		socket = daemon.SocketPath()
	}
	deadline := time.Now().Add(timeout)
	for {
		if daemon.SocketAlive(socket) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: socket %s did not accept connections within %s",
				ErrNotRunning, socket, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// resolveBinary picks the executable to spawn: the caller's override, then the
// running executable, then whatever `sonar` is on PATH.
func resolveBinary(binary string) (string, error) {
	if binary != "" {
		return binary, nil
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe, nil
	}
	exe, err := exec.LookPath("sonar")
	if err != nil {
		return "", fmt.Errorf("%w: cannot find the sonar binary to start it", ErrNotRunning)
	}
	return exe, nil
}

func trim(b []byte) string {
	const max = 400
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}
