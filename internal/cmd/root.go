package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/config"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

// loadedConfig holds the parsed config for the duration of a command run.
// Populated by PersistentPreRun; never nil after Execute starts.
var loadedConfig = &config.Config{}

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
		cfg, warnings := config.Load()
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, w)
		}
		loadedConfig = cfg

		// color: false in config disables ANSI; an explicit --no-color flag
		// also wins. We never force color ON (would corrupt piped output).
		if cfg.Color != nil && !*cfg.Color {
			display.NoColor = true
		}
		if nc, _ := cmd.Flags().GetBool("no-color"); nc {
			display.NoColor = true
		}

		ports.RegisterServices(cfg.Services)
	}
}

// errSilent asks for a non-zero exit without a further message: the command
// has already reported the failure itself, as `--json` output does.
var errSilent = errors.New("")

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
