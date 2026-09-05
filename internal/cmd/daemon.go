package cmd

import (
	"os"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/spf13/cobra"
)

// daemonCmd is the group for daemon inspection and control. The subcommands
// that talk to a running daemon live in daemon_ctl.go.
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Inspect and control the sonar daemon",
}

var daemonSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Print the daemon protocol JSON Schema",
	Long: "Print the JSON Schema describing every daemon type and method.\n" +
		"It is generated from the Go types and is byte-identical to the copy\n" +
		"checked in at docs/schema/protocol.schema.json.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := os.Stdout.Write(rpc.Marshal())
		return err
	},
}

func init() {
	daemonCmd.AddCommand(daemonSchemaCmd)
	rootCmd.AddCommand(daemonCmd)
}
