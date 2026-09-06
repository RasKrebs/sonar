package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// This file is the CLI's half of the two `--host` paths the remote-hosts
// design keeps side by side:
//
//   - A *registered* host has a daemon and a bridge. Its reads are forwarded
//     through the local daemon's `remote.call`, so they come back as the same
//     rows a local read does, already attributed and named.
//   - Anything else is the agentless fallback that has always been there:
//     ssh + ss/lsof, read-only, no daemon on either side.
//
// The rule is the spec's: "a registered host always uses the daemon path",
// and the fallback prints a hint to `sonar remote install`.

// remoteFallbackOnce keeps the install hint to one line per invocation.
var remoteFallbackOnce sync.Once

// routeRemote resolves what `--host <name>` should do. It returns a connected
// daemon client when the name is registered, and nil when the caller should
// take the agentless SSH path.
func routeRemote(ctx context.Context, name string) *client.Client {
	if name == "" || noDaemonFlag {
		return nil
	}
	c, err := dialDaemon(ctx)
	if err != nil {
		// No daemon, so no bridges: there is nothing to route through and the
		// SSH scan is the only thing that can answer.
		return nil
	}

	var res rpc.RemoteListResult
	if err := c.Call(ctx, "remote.list", rpc.Empty{}, &res); err != nil {
		c.Close()
		return nil
	}
	for _, h := range res.Hosts {
		if h.Name == name {
			return c
		}
	}
	c.Close()
	noteRemoteFallback(name)
	return nil
}

// noteRemoteFallback tells the user, once, that this host is being scanned the
// slow way and how to stop that.
func noteRemoteFallback(name string) {
	remoteFallbackOnce.Do(func() {
		fmt.Fprintf(os.Stderr,
			"note: %s is not a registered host — scanning it over ssh.\n"+
				"      `sonar remote add %s` registers it; `sonar remote install <name>` puts a daemon on it\n",
			name, name)
	})
}

// remoteCall forwards one method to a registered host's daemon and decodes the
// result. It is the CLI's use of `remote.call`: reads go through it unchanged,
// so a remote `ports.list` is the same call with the same params as a local
// one.
func remoteCall(ctx context.Context, c *client.Client, host, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var result rpc.RemoteCallResult
	if err := c.Call(ctx, "remote.call", rpc.RemoteCallParams{
		Host:   host,
		Method: method,
		Params: raw,
	}, &result); err != nil {
		return cliError(err)
	}
	if out == nil || len(result) == 0 {
		return nil
	}
	return json.Unmarshal(result, out)
}

// remoteHostNames is what shell completion offers for `--host`. It is silent
// when the daemon is not running.
func remoteHostNames(ctx context.Context) []string {
	hosts, err := readRemoteHosts(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(hosts)+1)
	names = append(names, state.LocalhostName)
	for _, h := range hosts {
		names = append(names, h.Name)
	}
	return names
}
