package cmd

import (
	"fmt"

	"github.com/raskrebs/sonar/internal/killer"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	killAllFilterFlag  string
	killAllProjectFlag string
	killAllYesFlag     bool
	killAllForceFlag   bool
)

// killAllCmd is an alias for `sonar kill --all`: it selects targets the same
// way and hands them to the same killer. (The deprecation notice lands with the
// rest of the migration work.)
var killAllCmd = &cobra.Command{
	Use:   "kill-all",
	Short: "Kill all processes matching a filter",
	Long: `Kill all listening processes, optionally filtered by type or Docker Compose project.

An alias for ` + "`sonar kill --all`" + `.

Examples:
  sonar kill-all --filter docker              # stop all Docker containers
  sonar kill-all --project myapp              # stop all containers in a compose project
  sonar kill-all --filter user                # kill all user processes
  sonar kill-all --filter docker --yes        # skip confirmation`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		Hint(cmd, HintKillAllToKill(killAllFilterFlag, killAllProjectFlag, killAllYesFlag, killAllForceFlag))
		opts := killer.Options{Force: killAllForceFlag}
		if onRemoteHost() {
			return killSweepThroughDaemon(cmd.Context(),
				func(snapshot []ports.ListeningPort) ([]killer.Target, error) {
					return sweepTargets(snapshot, killAllFilterFlag, killAllProjectFlag)
				}, opts, !killAllYesFlag, false)
		}

		snapshot := scanForKill()
		targets, err := sweepTargets(snapshot, killAllFilterFlag, killAllProjectFlag)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			fmt.Println("No matching ports found.")
			return nil
		}
		opts.Ports = snapshot
		return killRun(cmd.Context(), targets, snapshot, opts, !killAllYesFlag, false)
	},
}

func init() {
	killAllCmd.Flags().StringVar(&killAllFilterFlag, "filter", "", "Filter by type: docker, user, system")
	killAllCmd.Flags().StringVar(&killAllProjectFlag, "project", "", "Filter by Docker Compose project name")
	killAllCmd.Flags().BoolVarP(&killAllYesFlag, "yes", "y", false, "Skip confirmation prompt")
	killAllCmd.Flags().BoolVarP(&killAllForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	addHostFlag(killAllCmd, "Kill on a registered remote `host` instead of this machine")
	rootCmd.AddCommand(killAllCmd)
}
