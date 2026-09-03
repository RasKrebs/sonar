package install

import (
	"fmt"
	"os"
	"path/filepath"
)

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

// GitRoot returns the top level of the git working tree containing dir. It
// delegates to FindGitRoot and only rewords the failure, so skills and hooks
// resolve the repository root exactly the way the MCP installer does.
func GitRoot(dir string) (string, error) {
	root, err := FindGitRoot(dir)
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository (use --scope user)", dir)
	}
	return root, nil
}

// Home is the user's home directory, or "." if it cannot be determined.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
