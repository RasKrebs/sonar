package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Scope selects between the user's home configuration and the repository's.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeUser    Scope = "user"
)

// ParseScope validates a --scope flag value.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case ScopeProject:
		return ScopeProject, nil
	case ScopeUser:
		return ScopeUser, nil
	default:
		return "", fmt.Errorf("invalid scope %q: use project or user", s)
	}
}

// claudeDir is <home|root>/.claude for the given scope.
func claudeDir(scope Scope, home, root string) string {
	if scope == ScopeUser {
		return filepath.Join(home, ".claude")
	}
	return filepath.Join(root, ".claude")
}

// SkillPath is where the bundled skill lives for a scope.
func SkillPath(scope Scope, home, root string) string {
	return filepath.Join(claudeDir(scope, home, root), "skills", SkillName, "SKILL.md")
}

// SettingsPath is the Claude Code settings file for a scope.
func SettingsPath(scope Scope, home, root string) string {
	return filepath.Join(claudeDir(scope, home, root), "settings.json")
}

// GitRoot returns the top level of the git working tree containing dir.
func GitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository (use --scope user)", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// Home is the user's home directory, or "." if it cannot be determined.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
