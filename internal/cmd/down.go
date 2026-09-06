package cmd

import (
	"fmt"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/killer"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/profile"
	"github.com/spf13/cobra"
)

var (
	downYesFlag   bool
	downForceFlag bool
)

// downCmd is an alias for `sonar kill` over a profile's ports: it selects the
// targets and hands them to the same killer.
var downCmd = &cobra.Command{
	Use:   "down <profile>",
	Short: "Stop all ports listed in a profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		Hint(cmd, HintDownToKill(args[0]))
		prof, err := profile.Load(args[0])
		if err != nil {
			return err
		}

		selectTargets := func(snapshot []ports.ListeningPort) ([]killer.Target, error) {
			var targets []killer.Target
			for _, entry := range prof.Ports {
				for _, p := range snapshot {
					if p.Port == entry.Port {
						targets = append(targets, killer.Target{Port: p.Port, BindAddress: p.BindAddress})
					}
				}
			}
			return targets, nil
		}

		if onRemoteHost() {
			fmt.Printf("Profile %s on %s:\n", display.Bold(prof.Name), display.Bold(remoteHostFlag))
			return killSweepThroughDaemon(cmd.Context(), selectTargets,
				killer.Options{Force: downForceFlag}, !downYesFlag, false)
		}

		snapshot := scanForKill()
		targets, _ := selectTargets(snapshot)
		if len(targets) == 0 {
			fmt.Println("No profile ports are currently running.")
			return nil
		}

		fmt.Printf("Profile %s:\n", display.Bold(prof.Name))
		opts := killer.Options{Force: downForceFlag, Ports: snapshot}
		return killRun(cmd.Context(), targets, snapshot, opts, !downYesFlag, false)
	},
}

func init() {
	downCmd.Flags().BoolVarP(&downYesFlag, "yes", "y", false, "Skip confirmation prompt")
	downCmd.Flags().BoolVarP(&downForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	addHostFlag(downCmd, "Stop the profile's ports on a registered remote `host`")
	rootCmd.AddCommand(downCmd)
}
