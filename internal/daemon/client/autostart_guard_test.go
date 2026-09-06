package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon"
)

// Autostart is the one function in the tree that starts a process the caller
// did not ask for by name, so both of its refusals are pinned.

func TestAutostartHonoursTheEnvironmentSwitch(t *testing.T) {
	t.Setenv(NoAutostartEnv, "1")
	err := Autostart(context.Background(), filepath.Join(t.TempDir(), "sonar"), "")
	if err == nil {
		t.Fatal("Autostart ran with autostart disabled")
	}
	if !strings.Contains(err.Error(), NoAutostartEnv) {
		t.Errorf("error = %v, want it to name %s", err, NoAutostartEnv)
	}
	if !isNotRunning(err) {
		t.Errorf("error = %v, want it to wrap ErrNotRunning so callers still fall back", err)
	}
}

// The belt-and-braces guard: even with autostart allowed, a Go test binary is
// never spawned as the daemon. Handing it `serve --detach` would re-run the
// whole suite in the background instead (step 1A.20).
func TestAutostartRefusesATestBinary(t *testing.T) {
	t.Setenv(NoAutostartEnv, "")
	// It exits non-zero, so the opt-in case below fails fast on the spawn
	// rather than sitting out the three-second wait for a socket.
	fake := filepath.Join(t.TempDir(), "guardfake.test")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Autostart(context.Background(), fake, "")
	if err == nil {
		t.Fatal("Autostart spawned a Go test binary as the daemon")
	}
	if !strings.Contains(err.Error(), "test binary") || !strings.Contains(err.Error(), daemon.AllowTestDaemonEnv) {
		t.Errorf("error = %v, want it to explain the refusal and name the opt-in", err)
	}

	// The opt-in exists so a harness that really means it can say so.
	t.Setenv(daemon.AllowTestDaemonEnv, "1")
	err = Autostart(context.Background(), fake, filepath.Join(t.TempDir(), "d.sock"))
	if err != nil && strings.Contains(err.Error(), "test binary") {
		t.Errorf("the opt-in did not lift the guard: %v", err)
	}
}

func isNotRunning(err error) bool { return strings.Contains(err.Error(), ErrNotRunning.Error()) }
