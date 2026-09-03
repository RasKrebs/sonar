package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/install"
	"github.com/spf13/cobra"
)

// These flags belong to `install mcp`. They are not persistent on the parent:
// `install skills` and `install hooks` support a different, smaller set of
// clients and their own scope defaults, and inheriting these would have them
// silently accept --cursor or --codex and ignore them.
var (
	installClaudeCode bool
	installCursor     bool
	installCodex      bool
	installGeneric    bool
	installScope      string
	installPrint      bool
	installUninstall  bool
	installForce      bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Wire sonar into MCP clients and coding agents",
}

var installMCPCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Register sonar's MCP server with a client",
	Long: `Register sonar's MCP server with a client.

Writes {"command": "sonar", "args": ["mcp"]} into the client's configuration,
preserving every other server and key. Running it twice changes nothing.

Examples:
  sonar install mcp --claude-code               # merge into <git root>/.mcp.json
  sonar install mcp --claude-code --scope user  # claude mcp add --scope user
  sonar install mcp --cursor --scope user       # merge into ~/.cursor/mcp.json
  sonar install mcp --codex                     # codex mcp add
  sonar install mcp --generic --print           # print the JSON snippet
  sonar install mcp --claude-code --uninstall   # remove sonar's entry`,
	Args: cobra.NoArgs,
	RunE: installMCPRun,
}

func init() {
	installMCPCmd.Flags().BoolVar(&installClaudeCode, "claude-code", false, "Configure Claude Code")
	installMCPCmd.Flags().BoolVar(&installCursor, "cursor", false, "Configure Cursor")
	installMCPCmd.Flags().BoolVar(&installCodex, "codex", false, "Configure Codex (user scope only)")
	installMCPCmd.Flags().BoolVar(&installGeneric, "generic", false, "Print a snippet for any MCP client")
	installMCPCmd.Flags().StringVar(&installScope, "scope", "project", "Where to write: project or user")
	installMCPCmd.Flags().BoolVar(&installPrint, "print", false, "Print what would be written instead of touching disk")
	installMCPCmd.Flags().BoolVar(&installUninstall, "uninstall", false, "Remove what sonar wrote")
	installMCPCmd.Flags().BoolVar(&installForce, "force", false, "Replace an entry sonar did not write")

	installCmd.AddCommand(installMCPCmd)
	rootCmd.AddCommand(installCmd)
}

// selectedInstallClient turns the four mutually exclusive client flags into
// one Client.
func selectedInstallClient(claudeCode, cursor, codex, generic bool) (install.Client, error) {
	var chosen []install.Client
	if claudeCode {
		chosen = append(chosen, install.ClientClaudeCode)
	}
	if cursor {
		chosen = append(chosen, install.ClientCursor)
	}
	if codex {
		chosen = append(chosen, install.ClientCodex)
	}
	if generic {
		chosen = append(chosen, install.ClientGeneric)
	}
	switch len(chosen) {
	case 1:
		return chosen[0], nil
	case 0:
		return "", errors.New("pick one client: --claude-code, --cursor, --codex or --generic")
	default:
		return "", errors.New("only one client flag at a time")
	}
}

// installOptions builds the shared part of install.Options from the flags. M3
// reuses it for `install skills` and `install hooks`.
func installOptions(client install.Client) (install.Options, error) {
	scope, err := install.ParseScope(installScope)
	if err != nil {
		return install.Options{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return install.Options{}, fmt.Errorf("could not determine home directory: %w", err)
	}
	opts := install.Options{
		Client:    client,
		Scope:     scope,
		Print:     installPrint,
		Uninstall: installUninstall,
		Force:     installForce,
		Home:      home,
		Binary:    install.ResolveBinary(),
	}
	// Only project scope needs a git root; asking for it elsewhere would make
	// `--scope user` fail outside a repository for no reason.
	if scope == install.ScopeProject && client != install.ClientGeneric {
		cwd, err := os.Getwd()
		if err != nil {
			return install.Options{}, err
		}
		root, err := install.FindGitRoot(cwd)
		if err != nil {
			return install.Options{}, err
		}
		opts.GitRoot = root
	}
	return opts, nil
}

func installMCPRun(cmd *cobra.Command, args []string) error {
	client, err := selectedInstallClient(installClaudeCode, installCursor, installCodex, installGeneric)
	if err != nil {
		return err
	}
	// Codex has no project-scoped MCP config, so --codex on its own means user
	// scope rather than an error about the default value of --scope.
	if client == install.ClientCodex && !cmd.Flags().Changed("scope") {
		installScope = string(install.ScopeUser)
	}
	opts, err := installOptions(client)
	if err != nil {
		return err
	}

	res, err := install.InstallMCP(opts)
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if res.Output != "" {
		fmt.Print(res.Output)
	}
	if err != nil {
		return err
	}

	switch res.Action {
	case install.ActionCreated:
		fmt.Fprintf(os.Stderr, "created %s\n", res.Path)
	case install.ActionUpdated:
		fmt.Fprintf(os.Stderr, "updated %s\n", res.Path)
	case install.ActionUnchanged:
		fmt.Fprintf(os.Stderr, "%s already configured\n", res.Path)
	case install.ActionRemoved:
		fmt.Fprintf(os.Stderr, "removed sonar from %s\n", res.Path)
	case install.ActionAbsent:
		fmt.Fprintln(os.Stderr, "nothing to remove")
	case install.ActionRan:
		fmt.Fprintln(os.Stderr, "ok")
	}
	return nil
}
