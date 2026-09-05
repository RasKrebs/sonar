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

// Autostart spawns `sonar serve --detach` and waits, up to AutostartTimeout,
// for the socket to accept connections (contract §7). binary defaults to the
// running executable, so a client always starts the daemon it was built with.
func Autostart(ctx context.Context, binary, socket string) error {
	exe, err := resolveBinary(binary)
	if err != nil {
		return err
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
