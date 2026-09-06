package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/selfupdate"
	"github.com/spf13/cobra"
)

// rename, assign and history are write paths: the name and the pin live in the
// daemon's database and the history is written by its scanner, so unlike the
// read commands there is no direct-scan fallback. The daemon is autostarted;
// if that fails the command says why and where the log is.

var (
	renameClear bool
	renamePID   bool
	renameJSON  bool
)

var renameCmd = &cobra.Command{
	Use:   "rename <port|pid> [name]",
	Short: "Give a port a name of your own",
	Long: "The name is stored per machine and follows the service across\n" +
		"restarts, new PIDs and — for a `sonar start` run — new ports.\n" +
		"It is what every sonar surface shows from then on.",
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completePort,
	RunE:              renameRun,
}

func init() {
	renameCmd.Flags().BoolVar(&renameClear, "clear", false, "Remove the name and go back to the detected one")
	renameCmd.Flags().BoolVar(&renamePID, "pid", false, "Read the argument as a pid instead of a port")
	renameCmd.Flags().String("ip", "", "Specify bind address when a port is bound to multiple IPs")
	renameCmd.Flags().BoolVar(&renameJSON, "json", false, "Output as JSON")
	addHostFlag(renameCmd, "Rename a port on a registered remote `host` instead of this machine")
	rootCmd.AddCommand(renameCmd)
}

func renameRun(cmd *cobra.Command, args []string) error {
	sel, err := selectorFrom(args[0], renamePID, cmd)
	if err != nil {
		return err
	}
	name, err := writeValue(args, renameClear, "name", "sonar rename 3000 storefront")
	if err != nil {
		return err
	}

	c, err := connectForHostWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	var res rpc.PortsRenameResult
	if err := c.Call(cmd.Context(), "ports.rename",
		rpc.PortsRenameParams{Selector: sel, Name: name}, &res); err != nil {
		return daemonError(err)
	}
	if renameJSON {
		return writeJSON(res)
	}
	if name == nil {
		fmt.Printf("cleared the name for %s\n", display.Bold(args[0]))
		return nil
	}
	fmt.Printf("%s is now %s\n", args[0], display.Bold(*name))
	return nil
}

// selectorFrom turns the `<port|pid>` argument into a contract §3 selector.
func selectorFrom(arg string, asPID bool, cmd *cobra.Command) (rpc.Selector, error) {
	n, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || n <= 0 {
		return rpc.Selector{}, fmt.Errorf("%q is not a port or a pid", arg)
	}
	if asPID {
		return rpc.Selector{HostParams: hostParams(), PID: &n}, nil
	}
	sel := rpc.Selector{HostParams: hostParams(), Port: &n}
	if cmd != nil {
		if ip, _ := cmd.Flags().GetString("ip"); ip != "" {
			sel.BindAddress = &ip
		}
	}
	return sel, nil
}

// writeValue reads the new value from the arguments: the second argument, or
// null when --clear was given. Asking for both, or neither, is an error.
func writeValue(args []string, clear bool, what, example string) (*string, error) {
	switch {
	case clear && len(args) > 1:
		return nil, fmt.Errorf("--clear takes no %s", what)
	case clear:
		return nil, nil
	case len(args) < 2:
		return nil, fmt.Errorf("a %s is required (%s), or --clear to remove one", what, example)
	}
	value := strings.TrimSpace(args[1])
	if value == "" {
		return nil, fmt.Errorf("the %s is empty; use --clear to remove one", what)
	}
	return &value, nil
}

// connectForWrite dials the daemon, starting it if it is not running. A daemon
// that will not start is fatal here: there is nowhere else to write to.
//
// It is a variable for the same reason dialDaemon is: the tests point the CLI
// at a daemon of their own rather than at the user's.
var connectForWrite = func(ctx context.Context) (*client.Client, error) {
	c, err := client.Connect(ctx, client.ClientInfo{Name: "cli", Version: selfupdate.Version})
	if err != nil {
		return nil, fmt.Errorf("%w\nhint: the daemon log is at %s", err, daemon.LogPath())
	}
	return c, nil
}

// daemonError prints a daemon error the way the spec's error handling section
// asks: the detail, then the hint on its own line.
func daemonError(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Data.Hint == "" {
		return err
	}
	return fmt.Errorf("%s\nhint: %s", re.Data.Detail, re.Data.Hint)
}
