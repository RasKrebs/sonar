package cmd

import (
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/spf13/cobra"
)

var remoteRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Unregister a host and drop its rows",
	Long: `Unregister a host. Its SSH connection is torn down, its ports, groups and
sessions leave the state every client reads, and it is removed from the config.

Nothing on the remote host is changed: the daemon there keeps running until it
idles out on its own.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeRemoteHost,
	RunE:              runRemoteRemove,
}

func init() {
	remoteCmd.AddCommand(remoteRemoveCmd)
}

func runRemoteRemove(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	c, err := connectDaemonForRemote(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Call(cmd.Context(), "remote.remove", rpc.RemoteRemoveParams{Name: args[0]}, nil); err != nil {
		return cliError(err)
	}
	fmt.Fprintf(os.Stdout, "Removed %s.\n", args[0])
	return nil
}

// completeRemoteHost completes a registered host name. It stays silent when
// the daemon is not running: a completion is not the place to start one.
func completeRemoteHost(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	hosts, err := readRemoteHosts(cmd.Context())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		names = append(names, h.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
