package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/desktop"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/spf13/cobra"
)

var (
	installDesktopBase     string
	installDesktopVersion  string
	installDesktopDir      string
	installDesktopNoLaunch bool
	installDesktopJSON     bool
	installDesktopForce    bool
	installDesktopUpdate   bool
	installDesktopCheck    bool
	installDesktopDeb      bool
)

var installDesktopCmd = &cobra.Command{
	Use:   "desktop",
	Short: "Download and install the Sonar desktop app",
	Long: `Download and install the Sonar desktop app.

Sonar fetches a signed manifest of published builds, downloads the one for this
machine, checks its sha256 and size, and puts it where the system expects it:
/Applications on macOS (falling back to ~/Applications when that is not
writable — never sudo), ~/.local/opt/sonar-desktop on Linux, with a menu entry
and a sonar-desktop link in ~/.local/bin. Then it opens the app.

The app is not signed by Apple yet. Because sonar downloads it rather than a
browser, macOS attaches no quarantine attribute and it opens without a
Gatekeeper prompt — nothing here sets or strips quarantine.

Examples:
  sonar install desktop                       # install and launch
  sonar install desktop --no-launch           # install only
  sonar install desktop --update              # update, no-op if current
  sonar install desktop --check               # exit 1 if an update is available
  sonar install desktop --version 0.1.0-beta.1
  sonar install desktop --base https://example.com/sonar-desktop`,
	Args: cobra.NoArgs,
	// The report is the output. Without this, cobra prints its own "Error:"
	// line on top of the one Execute prints, and --check's silent non-zero
	// exit prints a bare "Error:" with nothing after it.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          installDesktopRun,
}

func init() {
	f := installDesktopCmd.Flags()
	f.StringVar(&installDesktopBase, "base", "", "Where to fetch desktop.json and its artifacts")
	f.StringVar(&installDesktopVersion, "version", "", "Install a specific version instead of the latest")
	f.StringVar(&installDesktopDir, "dir", "", "Install directory (default /Applications, or ~/.local/opt/sonar-desktop on Linux)")
	f.BoolVar(&installDesktopNoLaunch, "no-launch", false, "Install without opening the app")
	f.BoolVar(&installDesktopJSON, "json", false, "Print the result as JSON")
	f.BoolVar(&installDesktopForce, "force", false, "Ask a running Sonar to quit instead of refusing")
	f.BoolVar(&installDesktopUpdate, "update", false, "Update an existing install, doing nothing when it is current")
	f.BoolVar(&installDesktopCheck, "check", false, "Report installed and available versions; exit 1 when an update is available")
	f.BoolVar(&installDesktopDeb, "deb", false, "Linux: install the .deb through apt/dpkg instead of the AppImage")
	installCmd.AddCommand(installDesktopCmd)
}

func installDesktopRun(cmd *cobra.Command, args []string) error {
	opts := desktop.Options{
		Base: desktop.ResolveBase(
			installDesktopBase,
			os.Getenv(desktop.BaseEnv),
			loadedConfig.Desktop.DownloadBase,
		),
		Version:  installDesktopVersion,
		Dir:      installDesktopDir,
		NoLaunch: installDesktopNoLaunch,
		Force:    installDesktopForce,
		Update:   installDesktopUpdate,
		Check:    installDesktopCheck,
		Deb:      installDesktopDeb,
		Out:      os.Stderr,
		// A progress line is redrawn with carriage returns, which is noise in
		// a log and corruption in a JSON pipeline.
		Progress: !installDesktopJSON && isTerminal(os.Stderr),
	}

	res, err := desktop.Run(cmd.Context(), opts)
	if err != nil {
		var notAvailable *desktop.ErrNotAvailable
		if errors.As(err, &notAvailable) && installDesktopJSON {
			// Even the refusal is machine-readable under --json, so a
			// cross-platform installer script can branch on it.
			printJSON(map[string]any{"action": "unavailable", "error": err.Error()})
			return errSilent
		}
		return err
	}

	if installDesktopJSON {
		printJSON(res)
		if res.Action == desktop.ActionChecked && !res.UpToDate {
			return errSilent
		}
		return nil
	}
	return reportDesktopResult(res)
}

func reportDesktopResult(res *desktop.Result) error {
	switch res.Action {
	case desktop.ActionChecked:
		installed := res.InstalledVersion
		if installed == "" {
			installed = "not installed"
		} else if res.Path != "" {
			installed = fmt.Sprintf("%s (%s)", installed, res.Path)
		}
		fmt.Printf("installed:  %s\n", installed)
		fmt.Printf("available:  %s\n", res.Version)
		if res.UpToDate {
			fmt.Println(display.Dim("up to date"))
			return nil
		}
		fmt.Println("run `sonar install desktop --update` to update")
		return errSilent

	case desktop.ActionUpToDate:
		fmt.Printf("Sonar %s is already installed at %s\n", res.InstalledVersion, res.Path)
		return nil
	}

	for _, note := range res.Notes {
		fmt.Fprintln(os.Stderr, "note: "+note)
	}
	verb := "installed"
	if res.Action == desktop.ActionUpdated {
		verb = fmt.Sprintf("updated %s →", res.PreviousVersion)
	}
	fmt.Printf("Sonar %s %s at %s\n", verb, res.Version, res.Path)
	if res.Launched {
		fmt.Println("launched — it lives in the menu bar; `sonar tray` opens it again")
	} else if res.NotesURL != "" {
		fmt.Println("release notes: " + res.NotesURL)
	}
	return nil
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// isTerminal reports whether w is a character device, which is the only place
// a redrawing progress line makes sense.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
