package cmd

import (
	"context"
	"fmt"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/spf13/cobra"
)

// `--host <name>` on the write commands (remote-hosts spec, "CLI").
//
// The read commands (`list`, `info`, `watch`) have had a `--host` for as long
// as the agentless SSH scan has existed, and it still means what it meant: a
// registered host is read through the daemon, anything else is scanned over
// ssh. A write has no such fallback. `sonar kill --host box` is a kill on
// another machine's daemon, so the host has to be registered and a daemon has
// to be running here to reach it — and the command says so rather than quietly
// doing something else.
//
// One flag variable is enough: a process runs one command.
var remoteHostFlag string

// addHostFlag registers `--host` on a command that writes, with completion
// from the registered hosts.
func addHostFlag(cmd *cobra.Command, usage string) {
	cmd.Flags().StringVar(&remoteHostFlag, "host", "", usage)
	_ = cmd.RegisterFlagCompletionFunc("host",
		func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return remoteHostNames(cmd.Context()), cobra.ShellCompDirectiveNoFileComp
		})
}

// targetHost is the host the current command acts on, normalised: the empty
// string for this machine, so a params struct can carry it unconditionally.
func targetHost() string {
	if state.IsLocalhost(remoteHostFlag) {
		return ""
	}
	return remoteHostFlag
}

// hostParams is the wire form of the flag.
func hostParams() rpc.HostParams { return rpc.HostParams{Host: targetHost()} }

// onRemoteHost reports whether this command was pointed at another machine.
func onRemoteHost() bool { return targetHost() != "" }

// connectForHostWrite dials the daemon for a write that may be aimed at a
// remote host. Writing needs the daemon either way (contract §20); a remote
// write needs it because the bridge lives there, which is worth saying rather
// than falling back to a direct scan of the wrong machine.
func connectForHostWrite(ctx context.Context) (*client.Client, error) {
	if onRemoteHost() && noDaemonFlag {
		return nil, fmt.Errorf("--host %s needs the daemon: the connection to that host lives there\nhint: drop --no-daemon",
			remoteHostFlag)
	}
	return connectForWrite(ctx)
}

// hostSnapshot reads the port table a command selects its targets from, from
// whichever machine `--host` named.
func hostSnapshot(ctx context.Context, c *client.Client) ([]ports.ListeningPort, error) {
	return daemonList(ctx, c, rpc.PortsListParams{HostParams: hostParams(), All: true})
}
