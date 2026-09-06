package cmd

import "github.com/spf13/cobra"

// remoteCmd is the parent of the `sonar remote …` family (spec 3, "CLI"): the
// subcommands that register, list and set up the hosts sonar manages over SSH.
// It carries no flags of its own — each subcommand owns its own.
var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage the remote hosts sonar watches over SSH",
}

func init() {
	rootCmd.AddCommand(remoteCmd)
}
