package docker

import (
	"context"
	"os/exec"
	"time"
)

// CLITimeout bounds one `docker` invocation.
//
// The docker CLI talks to a daemon over a socket and has no timeout of its
// own: when Docker Desktop is starting, paused or wedged, `docker stats` does
// not fail, it waits. Every call here runs inside a scan, and a scan is what a
// `ports.kill` or a `.sonar.yaml` write is queued behind (contract §38), so an
// unbounded docker call is an unbounded reply. Ten seconds is far longer than
// a healthy `docker stats --no-stream` (about two) and short enough that a
// wedged daemon degrades to "no container stats this tick" instead of hanging
// the scan lock (contract §44).
const CLITimeout = 10 * time.Second

// cli builds a `docker` command that cannot outlive CLITimeout. The returned
// stop must be called once the output has been read.
func cli(args ...string) (cmd *exec.Cmd, stop func()) {
	ctx, cancel := context.WithTimeout(context.Background(), CLITimeout)
	return exec.CommandContext(ctx, "docker", args...), cancel
}

// output runs one `docker` command under CLITimeout and returns its stdout.
func output(args ...string) ([]byte, error) {
	cmd, stop := cli(args...)
	defer stop()
	return cmd.Output()
}
