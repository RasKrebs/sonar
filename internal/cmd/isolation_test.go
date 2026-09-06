package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/config"
	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/install"
	"github.com/raskrebs/sonar/internal/store"
	"github.com/raskrebs/sonar/internal/testenv"
	"github.com/spf13/cobra"
)

// This is the package that leaked. Its tests drive the real commands, so every
// path the CLI resolves has to land in the test's own temp directory — checked
// through the production resolvers, not through a restatement of them.
func TestEverythingTheCLIResolvesIsIsolated(t *testing.T) {
	testenv.RequireIsolated(t,
		config.Path(),
		store.Path(),
		daemon.ConfigDir(),
		daemon.SocketPath(),
		daemon.LogPath(),
		daemon.LockPath(),
		install.Home(),
	)

	// The live install is the thing these tests spent an afternoon writing to,
	// so it gets named explicitly rather than only implied by the temp check.
	if live := testenv.RealHome(); live != "" {
		if under(config.Path(), filepath.Join(live, ".config", "sonar")) {
			t.Fatalf("config.Path() = %s: the tests are pointed at the live install", config.Path())
		}
		if under(store.Path(), filepath.Join(live, ".config", "sonar")) {
			t.Fatalf("store.Path() = %s: the tests would write to the live database", store.Path())
		}
	}
}

func under(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// The autostart path is what forked `cmd.test serve --detach`. Two independent
// things now stop it, and both are checked here because either alone would
// have been enough to prevent the 2,150 leaked processes.
func TestAutostartIsRefusedInTests(t *testing.T) {
	// One: the process-wide ban TestMain sets.
	err := client.Autostart(context.Background(), "", daemon.SocketPath())
	if err == nil {
		t.Fatal("Autostart succeeded in a test binary")
	}
	if !strings.Contains(err.Error(), client.NoAutostartEnv) {
		t.Errorf("error = %v, want it to name %s", err, client.NoAutostartEnv)
	}

	// Two: even with the ban lifted, the executable is a test binary.
	testenv.AllowAutostart(t)
	err = client.Autostart(context.Background(), "", daemon.SocketPath())
	if err == nil {
		t.Fatal("Autostart succeeded with a Go test binary as the daemon")
	}
	if !strings.Contains(err.Error(), "test binary") {
		t.Errorf("error = %v, want it to say the binary is a test binary", err)
	}
}

// The production seam on the other side: `sonar serve` inside a test binary
// refuses rather than re-running the suite in the background.
func TestServeRefusesToRunAsATestBinary(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := serveRun(cmd, nil)
	if err == nil {
		t.Fatal("serve started inside a test binary")
	}
	if !strings.Contains(err.Error(), daemon.AllowTestDaemonEnv) {
		t.Errorf("error = %v, want it to name the opt-out %s", err, daemon.AllowTestDaemonEnv)
	}

	t.Setenv(daemon.AllowTestDaemonEnv, "1")
	if err := refuseTestBinaryDaemon(); err != nil {
		t.Errorf("the explicit opt-in did not lift the guard: %v", err)
	}
}

// Nothing here starts a daemon of its own, so nothing may be left running.
// TestMain checks this for the whole binary; this makes the failure land on a
// named test when it is this package's fault.
func TestNoDaemonSurvivesThisPackage(t *testing.T) {
	testenv.RequireNoLeakedDaemons(t)
}
