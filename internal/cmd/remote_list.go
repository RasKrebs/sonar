package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/spf13/cobra"
)

var remoteListJSON bool

var remoteListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "Show the registered hosts and their connections",
	Long: `Show every registered host: whether its bridge is up, how long a round trip
takes, what version of sonar it runs, and how loaded it is.

Status is one of connecting, connected, unreachable, outdated or incompatible.
An unreachable host keeps its row and keeps retrying; the reason column says
what ssh or the remote daemon reported.`,
	Args: cobra.NoArgs,
	RunE: runRemoteList,
}

func init() {
	remoteListCmd.Flags().BoolVar(&remoteListJSON, "json", false, "Output as JSON")
	remoteCmd.AddCommand(remoteListCmd)
}

func runRemoteList(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true

	hosts, err := readRemoteHosts(cmd.Context())
	if err != nil {
		return err
	}
	if remoteListJSON {
		return writeJSON(map[string]any{"hosts": hosts})
	}
	renderRemoteHosts(os.Stdout, hosts)
	return nil
}

// readRemoteHosts asks the daemon for the registered hosts. It never starts
// one: with no daemon there are no bridges, and "no hosts registered" would be
// a different, wrong answer from "cannot tell".
func readRemoteHosts(ctx context.Context) ([]state.Host, error) {
	c, err := dialDaemon(ctx)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			return nil, errors.New(
				"remote hosts need a running daemon; start one with `sonar serve --detach`")
		}
		return nil, err
	}
	defer c.Close()

	var res rpc.RemoteListResult
	if err := c.Call(ctx, "remote.list", rpc.Empty{}, &res); err != nil {
		return nil, cliError(err)
	}
	return res.Hosts, nil
}

func renderRemoteHosts(w io.Writer, hosts []state.Host) {
	if len(hosts) == 0 {
		fmt.Fprintln(w, "No remote hosts registered. `sonar remote add <user@host>` registers one.")
		return
	}
	fmt.Fprintf(w, "%-14s %-24s %-14s %-9s %-10s %-6s %s\n",
		display.Bold("NAME"), display.Bold("TARGET"), display.Bold("STATUS"),
		display.Bold("LATENCY"), display.Bold("VERSION"), display.Bold("PORTS"),
		display.Bold("LOAD"))
	for _, h := range hosts {
		fmt.Fprintf(w, "%-14s %-24s %-14s %-9s %-10s %-6d %s\n",
			display.Cyan(h.Name), h.Address, hostStatus(h.Status),
			remoteLatency(h), dashIfEmpty(h.DaemonVersion), h.Ports,
			hostLoad(h.Load))
		if h.StatusReason != nil && *h.StatusReason != "" && h.Status != state.HostConnected {
			fmt.Fprintf(w, "  %s %s\n", display.Dim("↳"), display.Dim(*h.StatusReason))
		}
	}
}

// remoteLatency prints the round trip, or a dash while the host is not
// answering — a stale number would read as a live one.
func remoteLatency(h state.Host) string {
	if h.Status != state.HostConnected || h.LatencyMs <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d ms", h.LatencyMs)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
