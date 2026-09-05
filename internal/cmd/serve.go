package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/selfupdate"
	"github.com/spf13/cobra"
)

var serveDetach bool

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the sonar daemon",
	Long: "Run the daemon that owns port scanning. Every interface (CLI, tray,\n" +
		"desktop app, MCP server) reads cached state and deltas from it instead\n" +
		"of scanning on its own.\n\n" +
		"Foreground by default, logging to stderr and to the log file.\n" +
		"`sonar daemon path` prints the socket it listens on.",
	Args: cobra.NoArgs,
	RunE: serveRun,
}

func init() {
	serveCmd.Flags().BoolVarP(&serveDetach, "detach", "d", false,
		"Start the daemon in the background and return once it accepts connections")
	rootCmd.AddCommand(serveCmd)
}

func serveRun(cmd *cobra.Command, _ []string) error {
	socket := daemon.SocketPath()

	if serveDetach {
		return detachDaemon(cmd.Context(), socket)
	}

	logger, closeLog, err := daemon.NewLogger(daemon.LogPath(),
		daemon.ParseLogLevel(loadedConfig.Daemon.ResolvedLogLevel()), true)
	if err != nil {
		return err
	}
	defer closeLog.Close()

	srv := daemon.New(daemon.Options{
		Socket:      socket,
		Version:     selfupdate.Version,
		IdleTimeout: loadedConfig.Daemon.ResolvedIdleTimeout(),
		Logger:      logger,
	})

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx); err != nil {
		if daemon.IsAlreadyRunning(err) {
			// Not a crash: another daemon is doing the job.
			fmt.Fprintln(os.Stderr, "error:", err)
			fmt.Fprintf(os.Stderr, "hint: `sonar daemon status` shows it, `sonar daemon restart` replaces it\n")
			os.Exit(1)
		}
		return err
	}
	return nil
}

// detachDaemon re-execs this binary as a background `sonar serve` and waits for
// the socket to come up, so the caller can rely on the daemon being ready when
// the command returns (contract §7: within 3 s).
func detachDaemon(ctx context.Context, socket string) error {
	if daemon.SocketAlive(socket) {
		pid := daemon.LockHolderPID(daemon.LockPath())
		if pid > 0 {
			return fmt.Errorf("daemon already running (pid %d)", pid)
		}
		return fmt.Errorf("daemon already running")
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating the sonar binary: %w", err)
	}

	// The child inherits nothing but the environment: its output goes to the
	// daemon log so a panic before the logger is up is still recoverable.
	if err := os.MkdirAll(filepath.Dir(daemon.LogPath()), 0o700); err != nil {
		return fmt.Errorf("creating the log directory: %w", err)
	}
	logFile, err := os.OpenFile(daemon.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", daemon.LogPath(), err)
	}
	defer logFile.Close()

	child := exec.Command(exe, "serve")
	child.Env = os.Environ()
	child.Stdin = nil
	child.Stdout = logFile
	child.Stderr = logFile
	detachProcess(child)

	if err := child.Start(); err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	// Do not wait for the child: it is the daemon. Release its resources.
	go func() { _ = child.Wait() }()

	if err := client.WaitForSocket(ctx, socket, client.AutostartTimeout); err != nil {
		return fmt.Errorf("%w (see %s)", err, daemon.LogPath())
	}
	fmt.Printf("sonar daemon started (pid %d), listening on %s\n", child.Process.Pid, socket)
	return nil
}

// waitForSocketGone is the inverse of WaitForSocket, used by `daemon restart`.
func waitForSocketGone(socket string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !daemon.SocketAlive(socket) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}
