package cmd

import (
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/install"
	"github.com/spf13/cobra"
)

var (
	installHooksClaudeCode bool
	installHooksScope      string
	installHooksMode       string
	installHooksPrint      bool
	installHooksUninstall  bool
)

var installHooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Install the sonar Claude Code hooks (optional)",
	Long: "Install two Claude Code hooks:\n\n" +
		"  SessionStart  exports SONAR_SESSION so every process this session\n" +
		"                starts is attributed to it\n" +
		"  PreToolUse    suggests `sonar start --` when a bare dev server is\n" +
		"                about to run (advise mode never blocks the command)\n\n" +
		"Entries sonar writes are tagged \"_sonar\": true, so --uninstall removes\n" +
		"exactly those and leaves your own hooks alone.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, err := install.ParseMode(installHooksMode)
		if err != nil {
			return err
		}
		if installHooksPrint {
			fragment, err := install.HookFragment(install.SonarBinary(), mode)
			if err != nil {
				return err
			}
			fmt.Println(fragment)
			return nil
		}
		if !installHooksClaudeCode {
			return fmt.Errorf("choose a client: --claude-code")
		}
		path, err := installTargetPath(installHooksScope, install.SettingsPath)
		if err != nil {
			return err
		}
		if installHooksUninstall {
			act, err := install.UninstallHooks(path)
			if err != nil {
				return err
			}
			reportAction(act, path, "hooks")
			return nil
		}
		act, warnings, err := install.InstallHooks(path, install.SonarBinary(), mode)
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, w)
		}
		if err != nil {
			return err
		}
		reportAction(act, path, "hooks")
		if act == install.ActionWrote {
			fmt.Println("Start a new Claude Code session to load the hooks.")
		}
		return nil
	},
}

func init() {
	installHooksCmd.Flags().BoolVar(&installHooksClaudeCode, "claude-code", false, "Install for Claude Code")
	installHooksCmd.Flags().StringVar(&installHooksScope, "scope", "project", "Where to install: project or user")
	installHooksCmd.Flags().StringVar(&installHooksMode, "mode", "advise", "advise (suggest and allow); strict is not implemented yet")
	installHooksCmd.Flags().BoolVar(&installHooksPrint, "print", false, "Print the settings fragment instead of writing it")
	installHooksCmd.Flags().BoolVar(&installHooksUninstall, "uninstall", false, "Remove what sonar installed")
	installCmd.AddCommand(installHooksCmd)
}
