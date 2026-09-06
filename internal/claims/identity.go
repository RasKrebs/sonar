package claims

import (
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/groups"
)

// Identity derives the project and worktree a claim belongs to from a working
// directory, letting an explicit project or worktree win over what the
// directory says (spec 2 §4).
//
// It lives here rather than in the CLI because two callers ask the same
// question from two different working directories: `sonar claim`, from the
// shell's cwd, and the `claim_port` MCP tool, from the directory the agent
// started the MCP server in. A key that differed between them would hand the
// same worktree two sets of ports, which is exactly what claims exist to
// prevent.
//
// A directory outside any git checkout claims under the directory's own name;
// a directory that is not a checkout and has no name at all claims under
// "sonar", so a key is never empty.
func Identity(cwd, project, worktree string) (string, string) {
	root, wt, ok := groups.Find(cwd)
	switch {
	case !ok:
		if project == "" {
			project = filepath.Base(cwd)
		}
	case wt != "":
		// groups.Find spells a linked worktree "<repo>@<name>".
		repo, name, _ := strings.Cut(wt, "@")
		if project == "" {
			project = repo
		}
		if worktree == "" {
			worktree = name
		}
	default:
		if project == "" {
			project = filepath.Base(root)
		}
	}
	if project == "" {
		project = "sonar"
	}
	if worktree == "" {
		worktree = DefaultWorktree
	}
	return project, worktree
}
