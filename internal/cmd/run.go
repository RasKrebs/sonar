package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/raskrebs/sonar/internal/runs"
	"github.com/spf13/cobra"
)

var (
	runTag string
	runID  string
)

var runCmd = &cobra.Command{
	Use:   "run --tag <label> [--id <runid>] -- <command> [args...]",
	Short: "Run a command, tagging its process so `sonar list` can attribute its ports",
	Long: "Spawn <command> with inherited stdio (transparent passthrough) and record\n" +
		"it in sonar's runs registry, keyed by PID. While it runs, `sonar list`\n" +
		"attributes any port opened by it (or its descendants) to the given --tag.\n" +
		"The registry entry is removed when the command exits.\n\n" +
		"Everything after -- is the command and its arguments, passed through verbatim.",
	// Args validated in RunE so we can give a clear message about the `--` separator.
	Args:                  cobra.ArbitraryArgs,
	DisableFlagsInUseLine: true,
	RunE:                  runRun,
}

func init() {
	runCmd.Flags().StringVar(&runTag, "tag", "", "Label to attribute this run's ports to (required)")
	runCmd.Flags().StringVar(&runID, "id", "", "Stable caller-supplied id (default: generated short id)")
	_ = runCmd.MarkFlagRequired("tag")
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	// cobra hands us everything after `--` as args. Require a command.
	if len(args) == 0 {
		return errors.New("no command given; usage: sonar run --tag <label> -- <command> [args...]")
	}

	Hint(cmd, HintRunToStart(runTag, args))

	id := runID
	if id == "" {
		id = generateShortID()
	}

	// Spawn with inherited stdio in the caller's cwd/env (transparent passthrough).
	child := exec.Command(args[0], args[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	// Leave Dir/Env unset so the child inherits the caller's cwd and environment.

	if err := child.Start(); err != nil {
		return fmt.Errorf("failed to start %q: %w", args[0], err)
	}

	pid := child.Process.Pid
	entry := runs.Entry{
		PID: pid,
		Tag: runTag,
		ID:  id,
		Cmd: strings.Join(args, " "),
	}
	if err := runs.Add(entry); err != nil {
		// Non-fatal: the command should still run even if tagging failed.
		fmt.Fprintf(os.Stderr, "sonar: warning: could not record run: %v\n", err)
	}

	// Always clean up the registry entry, regardless of how we exit.
	defer func() {
		if err := runs.Remove(pid); err != nil {
			fmt.Fprintf(os.Stderr, "sonar: warning: could not remove run entry: %v\n", err)
		}
	}()

	// Forward SIGINT/SIGTERM to the child so it can shut down cleanly; the wait
	// below then observes the child's exit and we reap its registry entry.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		for sig := range sigCh {
			if child.Process != nil {
				_ = child.Process.Signal(sig)
			}
		}
	}()

	// Wait for the child and propagate its exit code.
	err := child.Wait()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// SilenceUsage/Errors so cobra doesn't print usage on a passthrough failure;
		// we just mirror the child's exit code.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		os.Exit(exitErr.ExitCode())
	}
	return fmt.Errorf("running %q: %w", args[0], err)
}

// generateShortID returns a short random hex id (8 chars) for runs without an
// explicit --id. crypto/rand keeps it collision-resistant without a dep.
func generateShortID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a fixed-but-unique-ish id from the pid if rand fails.
		return fmt.Sprintf("run%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}
