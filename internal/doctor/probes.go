package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/raskrebs/sonar/internal/selfupdate"
)

// NetworkBudget is how long the doctor waits on anything off this machine.
// A check that runs out of budget is a `skip`: an offline machine is not a
// broken one, and a diagnosis must not itself hang.
const NetworkBudget = 2 * time.Second

// ReleaseProbe asks GitHub for the newest published release tag, within
// NetworkBudget.
func ReleaseProbe(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, NetworkBudget)
	defer cancel()
	tag, err := selfupdate.LatestTag(ctx)
	if err != nil {
		return "", fmt.Errorf("github.com did not answer within %s: %w", NetworkBudget, err)
	}
	return tag, nil
}

// DockerProbe asks the local docker daemon for its server version, within
// NetworkBudget. An error means the CLI is installed but its daemon is not
// answering — the state that quietly strips container names off every listing.
func DockerProbe(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, NetworkBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("`docker info` did not answer within %s", NetworkBudget)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.New(firstLine(string(exit.Stderr)))
		}
		return "", err
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", errors.New("`docker info` reported no server version")
	}
	return version, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
