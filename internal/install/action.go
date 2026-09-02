// Package install writes sonar's integration files into coding-agent
// configuration directories. Every writer takes an explicit path so callers
// and tests decide where files land.
package install

// BinaryName is the name of the sonar executable on PATH.
const BinaryName = "sonar"

// SkillName is the directory and frontmatter name of the bundled skill.
const SkillName = "sonar"

// Action reports what a writer did, so the CLI can print an honest one-liner.
type Action string

const (
	ActionWrote     Action = "wrote"     // file created or overwritten
	ActionUnchanged Action = "unchanged" // already exactly right
	ActionSkipped   Action = "skipped"   // present but not sonar-managed
	ActionRemoved   Action = "removed"   // uninstalled
	ActionAbsent    Action = "absent"    // nothing there to uninstall
)
