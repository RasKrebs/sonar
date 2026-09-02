package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Client is an MCP client sonar knows how to configure.
type Client string

const (
	ClientClaudeCode Client = "claude-code"
	ClientCursor     Client = "cursor"
	ClientCodex      Client = "codex"
	ClientGeneric    Client = "generic"
)

// Scope selects between a per-project config file and a per-user one.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
)

// TargetKind says how a client is configured: by editing a JSON file, by
// running the client's own CLI, or by printing a snippet for the user.
type TargetKind string

const (
	TargetFile    TargetKind = "file"
	TargetCommand TargetKind = "command"
	TargetSnippet TargetKind = "snippet"
)

// Target is where one (client, scope) pair keeps its MCP server configuration.
type Target struct {
	Kind TargetKind
	// Path is the config file, for TargetFile.
	Path string
	// Tool is the executable to look up on PATH, for TargetCommand.
	Tool string
	// Install and Uninstall are the argv to run, for TargetCommand. The sonar
	// binary path is substituted for binaryPlaceholder.
	Install   []string
	Uninstall []string
}

// binaryPlaceholder marks where the resolved sonar binary goes in a Target's
// argv, so the table below stays a plain data table.
const binaryPlaceholder = "{sonar}"

// ParseClient converts a flag value to a Client.
func ParseClient(s string) (Client, error) {
	switch Client(s) {
	case ClientClaudeCode, ClientCursor, ClientCodex, ClientGeneric:
		return Client(s), nil
	}
	return "", fmt.Errorf("unknown client %q: expected claude-code, cursor, codex or generic", s)
}

// ParseScope converts a flag value to a Scope; the empty string means project.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case "":
		return ScopeProject, nil
	case ScopeProject, ScopeUser:
		return Scope(s), nil
	}
	return "", fmt.Errorf("unknown scope %q: expected project or user", s)
}

// ResolveTarget maps a client and scope onto the file or command that
// configures it. gitRoot and home are passed in so the whole table is testable
// without touching the real filesystem.
func ResolveTarget(client Client, scope Scope, gitRoot, home string) (Target, error) {
	switch client {
	case ClientClaudeCode:
		if scope == ScopeUser {
			// Claude Code's user config lives in ~/.claude.json, which is not
			// a documented file; sonar drives the official CLI instead.
			return Target{
				Kind:      TargetCommand,
				Tool:      "claude",
				Install:   []string{"claude", "mcp", "add", "--scope", "user", "sonar", "--", binaryPlaceholder, "mcp"},
				Uninstall: []string{"claude", "mcp", "remove", "--scope", "user", "sonar"},
			}, nil
		}
		return Target{Kind: TargetFile, Path: filepath.Join(gitRoot, ".mcp.json")}, nil
	case ClientCursor:
		if scope == ScopeUser {
			return Target{Kind: TargetFile, Path: filepath.Join(home, ".cursor", "mcp.json")}, nil
		}
		return Target{Kind: TargetFile, Path: filepath.Join(gitRoot, ".cursor", "mcp.json")}, nil
	case ClientCodex:
		if scope == ScopeProject {
			return Target{}, fmt.Errorf("codex supports user scope only")
		}
		return Target{
			Kind:      TargetCommand,
			Tool:      "codex",
			Install:   []string{"codex", "mcp", "add", "sonar", "--", binaryPlaceholder, "mcp"},
			Uninstall: []string{"codex", "mcp", "remove", "sonar"},
		}, nil
	case ClientGeneric:
		return Target{Kind: TargetSnippet}, nil
	}
	return Target{}, fmt.Errorf("unknown client %q", client)
}

// FindGitRoot walks up from start looking for a .git entry. A linked worktree
// has a .git file rather than a directory, so both count.
func FindGitRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a git repository: %s", start)
		}
		dir = parent
	}
}

// ResolveBinary returns the command string to write into client configs: the
// bare name "sonar" when the running executable is what PATH resolves to,
// otherwise the running executable's absolute path.
func ResolveBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return "sonar"
	}
	return resolveBinary(exe, exec.LookPath)
}

func resolveBinary(exe string, lookPath func(string) (string, error)) string {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	onPath, err := lookPath("sonar")
	if err != nil {
		return exe
	}
	if resolved, err := filepath.EvalSymlinks(onPath); err == nil {
		onPath = resolved
	}
	if onPath == exe {
		return "sonar"
	}
	return exe
}
