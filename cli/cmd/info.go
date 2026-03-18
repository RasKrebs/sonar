package cmd

import (
	"fmt"
	"strconv"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var infoCmd = &cobra.Command{
	Use:   "info <port>",
	Short: "Show detailed information about a port",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		port, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid port: %s", args[0])
		}

		lp, err := ports.FindByPort(port)
		if err != nil {
			return err
		}

		// Enrich
		enriched := []ports.ListeningPort{*lp}
		docker.EnrichPorts(enriched)
		ports.Enrich(enriched)
		*lp = enriched[0]

		printField("Port", display.BoldCyan(fmt.Sprintf("%d", lp.Port)))
		printField("URL", display.Underline(lp.URL()))
		printField("Process", lp.Process)
		printField("PID", fmt.Sprintf("%d", lp.PID))
		printField("Type", lp.Type.String())

		if lp.Command != "" {
			printField("Command", lp.Command)
		}
		if lp.User != "" {
			printField("User", lp.User)
		}
		if lp.BindAddress != "" {
			printField("Bind Address", lp.BindAddress)
		}
		if lp.IPVersion != "" {
			printField("IP Version", lp.IPVersion)
		}

		fmt.Println()
		fmt.Println(display.Bold("Stats:"))
		printField("  CPU", fmt.Sprintf("%.1f%%", lp.CPUPercent))
		if lp.MemoryRSS > 0 {
			printField("  Memory", ports.FormatBytes(lp.MemoryRSS))
		}
		if lp.ThreadCount > 0 {
			printField("  Threads", fmt.Sprintf("%d", lp.ThreadCount))
		}
		if lp.Uptime != "" {
			printField("  Uptime", lp.Uptime)
		}
		if lp.State != "" {
			printField("  State", lp.State)
		}
		printField("  Connections", fmt.Sprintf("%d", lp.Connections))

		if lp.Type == ports.PortTypeDocker {
			fmt.Println()
			fmt.Println(display.Bold("Docker:"))
			if lp.DockerContainer != "" {
				printField("  Container", lp.DockerContainer)
			}
			if lp.DockerImage != "" {
				printField("  Image", lp.DockerImage)
			}
			if lp.DockerContainerPort > 0 {
				printField("  Container Port", fmt.Sprintf("%d", lp.DockerContainerPort))
			}
			if lp.DockerComposeService != "" {
				printField("  Compose Service", lp.DockerComposeService)
			}
			if lp.DockerComposeProject != "" {
				printField("  Compose Project", lp.DockerComposeProject)
			}
		}

		return nil
	},
}

func printField(label, value string) {
	fmt.Printf("%-16s %s\n", display.Dim(label+":"), value)
}

func init() {
	rootCmd.AddCommand(infoCmd)
}
