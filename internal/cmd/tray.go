package cmd

import (
	"github.com/raskrebs/sonar/internal/tray"
	"github.com/spf13/cobra"
)

var trayCmd = &cobra.Command{
	Use:   "tray",
	Short: "Launch the Sonar desktop app",
	Long: "Launch the Sonar desktop app, which lives in the menu bar or system tray\n" +
		"and shows every port, group and health check live.\n\n" +
		"If the app is not installed, sonar falls back to the older macOS menu bar\n" +
		"binary (sonar-tray) when it is present, and otherwise prints where to\n" +
		"download the app: " + tray.DownloadURL,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		detach, _ := cmd.Flags().GetBool("detach")
		return tray.Run(detach)
	},
}

func init() {
	trayCmd.Flags().BoolP("detach", "d", false, "Run the fallback menu bar binary in the background")
	rootCmd.AddCommand(trayCmd)
}
