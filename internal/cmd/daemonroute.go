package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/selfupdate"
	"github.com/raskrebs/sonar/internal/state"
)

// noDaemonFlag is the persistent --no-daemon flag: it forces the direct scan
// path for every read command, without the fallback note.
var noDaemonFlag bool

// fallbackNoteOnce keeps the "daemon unavailable" note to one line per
// invocation, however many times a command reaches for the daemon (spec,
// "Error handling").
var fallbackNoteOnce sync.Once

func noteFallback() {
	fallbackNoteOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "note: daemon unavailable, using direct scan")
	})
}

// dialDaemon is the seam the CLI tests replace. In production it is
// client.Connect, which autostarts `sonar serve --detach` (contract §7).
var dialDaemon = func(ctx context.Context) (*client.Client, error) {
	return client.Connect(ctx, client.ClientInfo{
		Name:    "cli",
		Version: selfupdate.Version,
	})
}

// daemonClient returns a connected client, or nil when the caller should fall
// back to a direct scan. A nil result with --no-daemon is silent; anything else
// prints the one-line note first.
func daemonClient(ctx context.Context) *client.Client {
	if noDaemonFlag {
		return nil
	}
	c, err := dialDaemon(ctx)
	if err != nil {
		noteFallback()
		return nil
	}
	return c
}

// daemonList reads the port table through the daemon and converts it back to
// the scanner rows every renderer takes, so a daemon-served table is the same
// table a direct scan prints.
func daemonList(ctx context.Context, c *client.Client, params rpc.PortsListParams) ([]ports.ListeningPort, error) {
	var res rpc.PortsListResult
	if err := c.Call(ctx, "ports.list", params, &res); err != nil {
		return nil, err
	}
	return state.ToListeningAll(res.Ports), nil
}

// listInclude maps the CLI's --stats / --health onto the wire's include.
func listInclude(stats, health bool) rpc.Include {
	inc := rpc.Include{}
	if stats {
		inc = append(inc, "stats")
	}
	if health {
		inc = append(inc, "health")
	}
	return inc
}

// strPtrOrNil is the helper the params builders share: an empty string means
// "no filter", which the wire spells as an absent field.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// cliError keeps a daemon error's hint, which the spec asks the CLI to print
// beneath the detail. rpc.Error's own Error() is only the detail, because the
// hint is a suggestion for a human, not part of the message.
func cliError(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data.Hint == "" {
		return err
	}
	return fmt.Errorf("%s\nhint: %s", re.Data.Detail, re.Data.Hint)
}
