package cmd

import (
	"fmt"
	"strings"

	"github.com/rkrebs/sonar/internal/display"
	"github.com/rkrebs/sonar/internal/docker"
	"github.com/rkrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	killAllFilterFlag  string
	killAllProjectFlag string
	killAllYesFlag     bool
	killAllForceFlag   bool
)

var killAllCmd = &cobra.Command{
	Use:   "kill-all",
	Short: "Kill all processes matching a filter",
	Long: `Kill all listening processes, optionally filtered by type or Docker Compose project.

Examples:
  sonar kill-all --filter docker              # stop all Docker containers
  sonar kill-all --project myapp              # stop all containers in a compose project
  sonar kill-all --filter user                # kill all user processes
  sonar kill-all --filter docker --yes        # skip confirmation`,
	RunE: func(cmd *cobra.Command, args []string) error {
		results, err := ports.Scan()
		if err != nil {
			return err
		}

		docker.EnrichPorts(results)
		ports.Enrich(results)

		if !allFlag {
			results = excludeApps(results)
		}

		if killAllFilterFlag != "" {
			results = display.FilterPorts(results, killAllFilterFlag)
		}

		if killAllProjectFlag != "" {
			results = filterByProject(results, killAllProjectFlag)
		}

		if len(results) == 0 {
			fmt.Println("No matching ports found.")
			return nil
		}

		// Show what will be killed
		fmt.Printf("Will kill %d process(es):\n", len(results))
		for _, p := range results {
			name := display.Bold(p.DisplayName())
			fmt.Printf("  - %s on port %d\n", name, p.Port)
		}

		if !killAllYesFlag {
			fmt.Print("\nProceed? [y/N] ")
			var answer string
			fmt.Scanln(&answer)
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println("Aborted.")
				return nil
			}
		}

		var errors []string
		killed := 0
		for _, p := range results {
			if p.Type == ports.PortTypeDocker {
				name := p.DockerContainer
				if p.DockerComposeService != "" {
					name = p.DockerComposeService
				}
				fmt.Printf("Stopping Docker container %s on port %d\n",
					display.Bold(name), p.Port)
				if err := docker.StopContainer(p.DockerContainer); err != nil {
					errors = append(errors, fmt.Sprintf("port %d: %v", p.Port, err))
					continue
				}
			} else {
				sigName := "SIGTERM"
				if killAllForceFlag {
					sigName = "SIGKILL"
				}
				fmt.Printf("Killing %s (PID %d) on port %d with %s\n",
					display.Bold(p.DisplayName()), p.PID, p.Port, sigName)
				if err := ports.KillPID(p.PID, killAllForceFlag); err != nil {
					errors = append(errors, fmt.Sprintf("port %d: %v", p.Port, err))
					continue
				}
			}
			fmt.Printf("Freed %s\n", display.Underline(p.URL()))
			killed++
		}

		fmt.Printf("\n%d/%d processes killed.\n", killed, len(results))
		if len(errors) > 0 {
			return fmt.Errorf("some processes failed to kill:\n  %s", strings.Join(errors, "\n  "))
		}
		return nil
	},
}

func filterByProject(pp []ports.ListeningPort, project string) []ports.ListeningPort {
	var result []ports.ListeningPort
	for _, p := range pp {
		if strings.EqualFold(p.DockerComposeProject, project) {
			result = append(result, p)
		}
	}
	return result
}

func init() {
	killAllCmd.Flags().StringVar(&killAllFilterFlag, "filter", "", "Filter by type: docker, user, system")
	killAllCmd.Flags().StringVar(&killAllProjectFlag, "project", "", "Filter by Docker Compose project name")
	killAllCmd.Flags().BoolVarP(&killAllYesFlag, "yes", "y", false, "Skip confirmation prompt")
	killAllCmd.Flags().BoolVarP(&killAllForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	rootCmd.AddCommand(killAllCmd)
}
