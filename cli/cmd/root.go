package cmd

import (
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/spf13/cobra"
)

const banner = `
  ███████╗ ██████╗ ███╗   ██╗ █████╗ ██████╗
  ██╔════╝██╔═══██╗████╗  ██║██╔══██╗██╔══██╗
  ███████╗██║   ██║██╔██╗ ██║███████║██████╔╝
  ╚════██║██║   ██║██║╚██╗██║██╔══██║██╔══██╗
  ███████║╚██████╔╝██║ ╚████║██║  ██║██║  ██║
  ╚══════╝ ╚═════╝ ╚═╝  ╚═══╝╚═╝  ╚═╝╚═╝  ╚═╝
`

var rootCmd = &cobra.Command{
	Use:   "sonar",
	Short: "Detect services listening on localhost ports",
	Long:  display.Cyan(banner) + "\n  " + display.Dim("Detect and manage services listening on localhost ports."),
	// No RunE — bare `sonar` shows help
}

func init() {
	rootCmd.PersistentFlags().Bool("no-color", false, "Disable colored output")
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if nc, _ := cmd.Flags().GetBool("no-color"); nc {
			display.NoColor = true
		}
	}
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
