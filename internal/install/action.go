// Package install writes sonar's integration files into coding-agent
// configuration directories. Every writer takes an explicit path so callers
// and tests decide where files land.
package install

// BinaryName is the name of the sonar executable on PATH.
const BinaryName = "sonar"

// SkillName is the directory and frontmatter name of the bundled skill.
const SkillName = "sonar"

// Action reports what a writer did, so the CLI can print an honest one-liner.
// The file writers (skills, hooks) and the MCP config merger share the type;
// each uses the subset of values that can describe its own outcomes.
type Action string

const (
	// Shared by every writer.
	ActionUnchanged Action = "unchanged" // already exactly right
	ActionRemoved   Action = "removed"   // uninstalled
	ActionAbsent    Action = "absent"    // nothing there to uninstall

	// Reported by the skill and hook writers.
	ActionWrote   Action = "wrote"   // file created or overwritten
	ActionSkipped Action = "skipped" // present but not sonar-managed

	// Reported by InstallMCP, which distinguishes creating a client's config
	// file from merging into one that already exists, and can defer the work
	// to the client's own CLI.
	ActionCreated Action = "created"
	ActionUpdated Action = "updated"
	ActionPrinted Action = "printed"
	ActionRan     Action = "ran"
)
