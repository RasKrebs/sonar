package cmd

import (
	"github.com/spf13/cobra"

	// The remote-host connection manager registers its RPC handlers and its
	// OnStart hook from its own init(), so `sonar serve` in this binary knows
	// how to bridge to a registered host (contract §8).
	_ "github.com/raskrebs/sonar/internal/remote"
)

// remoteCmd is the parent of the `sonar remote …` family (spec 3, "CLI"): the
// subcommands that register, list and set up the hosts sonar manages over SSH.
// It carries no flags of its own — each subcommand owns its own.
var remoteCmd = &cobra.Command{
	Use:   "remote",
	Short: "Manage the remote hosts sonar watches over SSH",
	Long: `Manage the machines sonar watches besides this one.

A registered host runs the same daemon; the local daemon keeps one SSH
connection to it and multiplexes its ports, groups and load into the state the
CLI and the app already read. Every row carries the host it came from, so
'sonar list --host hetzner' and the app's host switcher are the same data.

Host names are lowercase letters, digits and dashes. The target is what ssh
receives, verbatim: a user@host, or an alias from your ~/.ssh/config.`,
}

func init() {
	rootCmd.AddCommand(remoteCmd)
}
