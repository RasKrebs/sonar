package cmd

import (
	"fmt"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/selfupdate"
	"github.com/spf13/cobra"
)

var checkFlag bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of sonar",
	RunE:  versionRun,
}

func init() {
	versionCmd.Flags().BoolVar(&checkFlag, "check", false, "Check if a newer version is available")
	rootCmd.AddCommand(versionCmd)
}

func versionRun(cmd *cobra.Command, args []string) error {
	fmt.Println(selfupdate.VersionString())

	if !checkFlag {
		return nil
	}

	release, err := selfupdate.FetchLatestRelease()
	if err != nil {
		return err
	}

	if selfupdate.IsNewer(selfupdate.Version, release.TagName) {
		fmt.Println(display.Yellow(fmt.Sprintf("A newer version is available: %s", release.TagName)))
		fmt.Println(display.Dim("Run 'sonar update' to upgrade"))
	} else {
		fmt.Println(display.Green("You are running the latest version"))
	}

	return nil
}
