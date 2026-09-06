package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/remoteinstall"
	"github.com/spf13/cobra"
)

var (
	remoteInstallName      string
	remoteInstallVersion   string
	remoteInstallIdentity  string
	remoteInstallSSHArgs   []string
	remoteInstallNoService bool
	remoteInstallJSON      bool
)

var remoteInstallCmd = &cobra.Command{
	Use:   "install <user@host>",
	Short: "Install sonar on a remote host over SSH and start its daemon",
	Long: `Install sonar on a host you can already ssh to, and start its daemon there.

The release archive is downloaded and checksummed on the remote host itself —
nothing is copied from this machine — and the binary lands in ~/.local/bin, so
no part of this needs root. The daemon is started as a systemd user unit where
there is one, and with ` + "`sonar serve --detach`" + ` where there is not.

The version installed is the version of this sonar, so both ends speak the same
protocol. Re-running the command upgrades in place and restarts the daemon.

The target is handed to ssh untouched, so a Host alias from ~/.ssh/config works
and so do the ProxyJump, IdentityFile and Port it sets.

Examples:
  sonar remote install deploy@203.0.113.7
  sonar remote install hetzner --version v0.6.0
  sonar remote install deploy@box --identity ~/.ssh/id_ed25519
  sonar remote install deploy@box --ssh-arg -J --ssh-arg bastion
  sonar remote install deploy@box --no-service    # the binary only`,
	Args: cobra.ExactArgs(1),
	RunE: remoteInstallRun,
}

func init() {
	f := remoteInstallCmd.Flags()
	f.StringVar(&remoteInstallName, "name", "", "Name to register the host under (default: derived from the target)")
	f.StringVar(&remoteInstallVersion, "version", "", "Release to install (default: this sonar's version)")
	f.StringVarP(&remoteInstallIdentity, "identity", "i", "", "SSH key file to authenticate with")
	f.StringArrayVar(&remoteInstallSSHArgs, "ssh-arg", nil, "Extra argument for ssh (repeatable)")
	f.BoolVar(&remoteInstallNoService, "no-service", false, "Install the binary without starting the daemon")
	f.BoolVar(&remoteInstallJSON, "json", false, "Output as JSON")
	remoteCmd.AddCommand(remoteInstallCmd)
}

// remoteInstallRun does the install in this process rather than through the
// daemon, even though `remote.install` exists on the wire.
//
// ssh is the reason: a first connection asks about the host key, a key without
// an agent asks for its passphrase, and both prompts need the terminal the user
// is sitting at. A daemon has none, so it runs ssh with BatchMode=yes and would
// fail on exactly the setup step this command exists to get past. The desktop,
// which cannot prompt either way, is what the streaming method is for.
func remoteInstallRun(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	opts := remoteinstall.Options{
		Target:    strings.TrimSpace(args[0]),
		Name:      remoteInstallName,
		Version:   remoteInstallVersion,
		Identity:  remoteInstallIdentity,
		SSHArgs:   remoteInstallSSHArgs,
		NoService: remoteInstallNoService,
	}

	res, err := remoteinstall.Install(cmd.Context(), opts, func(step, detail string) {
		if !remoteInstallJSON {
			printInstallStep(opts.Target, step, detail)
		}
	})
	if err != nil {
		return err
	}

	if remoteInstallJSON {
		return writeJSON(remoteinstall.End(res))
	}
	printInstallSummary(res)
	return nil
}

// printInstallStep turns one progress step into the line a human reads. The
// steps are a fixed vocabulary, so the wording lives here rather than being
// shipped over the wire from the remote script.
func printInstallStep(target, step, detail string) {
	mark := display.Dim("·")
	switch step {
	case remoteinstall.StepConnect:
		fmt.Printf("%s connecting to %s\n", mark, display.Bold(target))
	case remoteinstall.StepDetect:
		fmt.Printf("%s remote is %s\n", mark, display.Bold(detail))
	case remoteinstall.StepResolve:
		fmt.Printf("%s installing %s\n", mark, display.Bold(detail))
	case remoteinstall.StepDownload:
		fmt.Printf("%s downloading %s\n", mark, display.Dim(detail))
	case remoteinstall.StepVerify:
		fmt.Printf("%s verifying sha256 %s\n", mark, display.Dim(short(detail, 16)))
	case remoteinstall.StepExtract:
		fmt.Printf("%s extracting %s\n", mark, display.Dim(detail))
	case remoteinstall.StepInstall:
		fmt.Printf("%s installing %s\n", mark, display.Dim(detail))
	case remoteinstall.StepService:
		fmt.Printf("%s %s\n", mark, serviceLine(detail))
	case remoteinstall.StepLinger:
		fmt.Fprintf(os.Stderr, "%s the daemon stops when you log out; run: %s\n",
			display.Yellow("note:"), display.Bold(detail))
	case remoteinstall.StepCheck:
		fmt.Printf("%s verifying the install\n", mark)
	case remoteinstall.StepRemote:
		fmt.Printf("  %s\n", display.Dim(detail))
	case remoteinstall.StepDone:
		// The summary says it better.
	default:
		fmt.Printf("%s %s %s\n", mark, step, display.Dim(detail))
	}
}

func serviceLine(mode string) string {
	switch mode {
	case remoteinstall.ServiceSystemd:
		return "started the systemd user unit ~/.config/systemd/user/sonar.service"
	case remoteinstall.ServiceDetached:
		return "started the daemon detached (no systemd user session)"
	case remoteinstall.ServiceNone:
		return "not starting the daemon (--no-service)"
	default:
		return "service: " + mode
	}
}

func printInstallSummary(res *remoteinstall.Result) {
	fmt.Printf("\n%s sonar %s on %s (%s/%s) at %s\n",
		display.Green("✓"), display.Bold(res.Version), display.Bold(res.Target),
		res.OS, res.Arch, res.BinPath)
	switch {
	case res.DaemonRunning && res.DaemonPID > 0:
		fmt.Printf("  daemon running (pid %d), started by %s\n", res.DaemonPID, res.Service)
	case res.DaemonRunning:
		fmt.Printf("  daemon running, started by %s\n", res.Service)
	default:
		fmt.Printf("  daemon not started; `ssh %s sonar serve --detach` starts it\n", res.Target)
	}
	fmt.Printf("  %s\n", display.Dim(fmt.Sprintf("register it with `sonar remote add %s %s`", res.Name, res.Target)))
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
