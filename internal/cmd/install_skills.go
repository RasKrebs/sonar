package cmd

import (
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/install"
	"github.com/spf13/cobra"
)

var (
	installSkillsClaudeCode bool
	installSkillsScope      string
	installSkillsPrint      bool
	installSkillsUninstall  bool
	installSkillsForce      bool
)

var installSkillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "Install the bundled sonar skill for a coding agent",
	Long: "Install the bundled sonar skill.\n\n" +
		"It teaches the agent to start servers with `sonar start --`, wait for\n" +
		"ports instead of sleeping, and clean up what it started.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if installSkillsPrint {
			fmt.Print(install.SkillContent())
			return nil
		}
		if !installSkillsClaudeCode {
			return fmt.Errorf("choose a client: --claude-code")
		}
		path, err := installTargetPath(installSkillsScope, install.SkillPath)
		if err != nil {
			return err
		}
		if installSkillsUninstall {
			act, err := install.UninstallSkill(path)
			if err != nil {
				return err
			}
			reportAction(act, path, "skill")
			return nil
		}
		act, err := install.InstallSkill(path, installSkillsForce)
		if err != nil {
			return err
		}
		reportAction(act, path, "skill")
		if act == install.ActionWrote {
			fmt.Println("Start a new Claude Code session to pick up /sonar.")
		}
		return nil
	},
}

func init() {
	installSkillsCmd.Flags().BoolVar(&installSkillsClaudeCode, "claude-code", false, "Install for Claude Code")
	installSkillsCmd.Flags().StringVar(&installSkillsScope, "scope", "user", "Where to install: project or user")
	installSkillsCmd.Flags().BoolVar(&installSkillsPrint, "print", false, "Print the skill to stdout instead of writing it")
	installSkillsCmd.Flags().BoolVar(&installSkillsUninstall, "uninstall", false, "Remove what sonar installed")
	installSkillsCmd.Flags().BoolVar(&installSkillsForce, "force", false, "Overwrite a file sonar did not write")
	installCmd.AddCommand(installSkillsCmd)
}

// installTargetPath resolves a --scope value into a concrete path using one of
// the install package's path builders.
func installTargetPath(scopeFlag string, build func(install.Scope, string, string) string) (string, error) {
	scope, err := install.ParseScope(scopeFlag)
	if err != nil {
		return "", err
	}
	root := ""
	if scope == install.ScopeProject {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if root, err = install.GitRoot(wd); err != nil {
			return "", err
		}
	}
	return build(scope, install.Home(), root), nil
}

func reportAction(act install.Action, path, what string) {
	switch act {
	case install.ActionWrote:
		fmt.Printf("wrote %s\n", path)
	case install.ActionUnchanged:
		fmt.Printf("%s already up to date at %s\n", what, path)
	case install.ActionRemoved:
		fmt.Printf("removed the sonar %s from %s\n", what, path)
	case install.ActionAbsent:
		fmt.Printf("no sonar %s installed at %s\n", what, path)
	case install.ActionSkipped:
		fmt.Printf("left %s alone\n", path)
	}
}
