package remoteinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// sshBinary is the program every remote command goes through. It is a variable
// only so a test can point it somewhere else; the shipped value is the system
// ssh, which is what makes `~/.ssh/config`, ProxyJump, agents and
// known_hosts behave exactly as they do when the user types ssh themselves.
var sshBinary = "ssh"

// runner builds the `ssh` invocations for one target.
type runner struct {
	// target is the ssh destination, verbatim: "user@host", or a Host alias
	// from ~/.ssh/config. sonar never resolves it itself.
	target string
	// identity is an optional key file (-i).
	identity string
	// extra are additional ssh flags, in front of the destination.
	extra []string
	// batch adds -o BatchMode=yes, so ssh fails instead of blocking on a
	// password or passphrase prompt. The daemon sets it: it has no terminal to
	// prompt on, and a hung install would be indistinguishable from a slow one.
	batch bool
}

// args builds the argv for one remote command. The command is passed as a
// single argument after "--", so a destination that starts with a dash can
// never be read as a flag and the remote login shell sees exactly one string.
func (r runner) args(remoteCmd string) []string {
	argv := make([]string, 0, 8+len(r.extra))
	if r.batch {
		argv = append(argv, "-o", "BatchMode=yes")
	}
	if r.identity != "" {
		argv = append(argv, "-i", expandHome(r.identity))
	}
	argv = append(argv, r.extra...)
	argv = append(argv, "--", r.target, remoteCmd)
	return argv
}

// command returns the exec.Cmd for one remote command.
func (r runner) command(ctx context.Context, remoteCmd string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, sshBinary, r.args(remoteCmd)...)
	// A remote process that kept the session's stdout open would otherwise
	// keep Wait blocked after ssh itself is gone. The daemon the install
	// starts redirects its own output, but a host with a chatty login shell
	// or a hook of the user's own must not hang the install.
	cmd.WaitDelay = waitDelay
	return cmd
}

// waitDelay bounds how long Wait may block on a pipe after ssh has exited.
const waitDelay = 10 * time.Second

// output runs a remote command and returns its stdout. stderr is folded into
// the error, because that is where both ssh and the remote shell explain
// themselves ("Permission denied", "command not found").
func (r runner) output(ctx context.Context, remoteCmd string) (string, error) {
	cmd := r.command(ctx, remoteCmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), sshError(r.target, err, stderr.String())
	}
	return stdout.String(), nil
}

// stream runs a remote command, feeding it stdin and handing every stdout line
// to onLine as it arrives. The remote's stderr is buffered and folded into the
// error, so a failure explains itself without interleaving into the progress.
func (r runner) stream(ctx context.Context, remoteCmd string, stdin io.Reader, onLine func(string)) error {
	cmd := r.command(ctx, remoteCmd)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("running ssh: %w", err)
	}
	scanLines(pipe, onLine)
	if err := cmd.Wait(); err != nil {
		return sshError(r.target, err, stderr.String())
	}
	return nil
}

// sshError puts the remote's own words in front. ssh's exit code alone
// ("exit status 255") says nothing a user can act on.
func sshError(target string, err error, stderr string) error {
	msg := firstErrorLine(stderr)
	if msg == "" {
		return fmt.Errorf("ssh %s: %w", target, err)
	}
	return fmt.Errorf("ssh %s: %s", target, msg)
}

// firstErrorLine picks the line worth showing out of a remote stderr: the
// script's own die() message when there is one, the last non-empty line
// otherwise.
func firstErrorLine(stderr string) string {
	var last string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if detail, ok := strings.CutPrefix(line, ErrorMarker+"\t"); ok {
			return strings.TrimSpace(detail)
		}
		last = strings.TrimSpace(line)
	}
	return last
}

// expandHome resolves a leading ~ in an identity path. ssh would do it itself
// for a path inside its config, but not for one we hand it on the command line.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
