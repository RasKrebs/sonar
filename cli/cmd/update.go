package cmd

import (
	"fmt"
	"runtime"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/selfupdate"
	"github.com/spf13/cobra"
)

var dryRunFlag bool

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update sonar to the latest version",
	RunE:  updateRun,
}

func init() {
	updateCmd.Flags().BoolVar(&dryRunFlag, "dry-run", false, "Show what would happen without making changes")
	rootCmd.AddCommand(updateCmd)
}

func updateRun(cmd *cobra.Command, args []string) error {
	if selfupdate.IsHomebrew() {
		fmt.Println("sonar was installed via Homebrew. Please update with:")
		fmt.Println(display.Bold("  brew upgrade sonar"))
		return nil
	}

	fmt.Println("Checking for updates...")

	release, err := selfupdate.FetchLatestRelease()
	if err != nil {
		return err
	}

	if !selfupdate.IsNewer(selfupdate.Version, release.TagName) {
		fmt.Println(display.Green("You are already running the latest version: " + selfupdate.Version))
		return nil
	}

	assetURL, err := selfupdate.FindAssetURL(release)
	if err != nil {
		return err
	}

	if dryRunFlag {
		fmt.Printf("Would update sonar %s -> %s\n", selfupdate.Version, release.TagName)
		fmt.Printf("Download: %s\n", assetURL)
		return nil
	}

	fmt.Printf("Downloading sonar %s for %s...\n", release.TagName, display.Dim(fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)))

	if err := selfupdate.DownloadAndReplace(assetURL); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	fmt.Println(display.Green(fmt.Sprintf("Updated sonar %s -> %s", selfupdate.Version, release.TagName)))
	return nil
}
