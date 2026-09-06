package cmd

import (
	"github.com/spf13/cobra"
)

// relayCmd is the parent of `sonar relay …`: the server side of sonar, run by
// us and self-hosted by anyone who would rather keep their own data.
//
// It is not the daemon and shares nothing with it — no socket, no protocol, no
// database. It lives in the same binary so a deployment is one artefact.
var relayCmd = &cobra.Command{
	Use:   "relay",
	Short: "Run the sonar relay: the server side of sonar",
	Long: `Run the sonar relay.

The relay is a small HTTP service, hosted by us and published as a Docker
image (ghcr.io/raskrebs/sonar-relay) so you can run your own. Today it collects
anonymous product telemetry; it is the same service that will later terminate
exposed tunnels and hold sign-in.

It has nothing to do with the local daemon: 'sonar serve' watches your ports,
'sonar relay serve' answers HTTP for a fleet. docs/RELAY.md has the deployment.`,
}

func init() {
	rootCmd.AddCommand(relayCmd)
}
