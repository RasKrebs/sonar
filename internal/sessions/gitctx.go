package sessions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/state"
)

// GitContext is the checkout a command is started in: the worktree name and
// the branch, as spec 2 §3 defines them.
//
// worktree is the base name of the checkout when it is a *linked* worktree —
// `git worktree add` — and empty for the primary checkout, so a badge reading
// "worktree feature-x" only ever appears where there really is one. branch is
// the checked-out branch, or the short commit for a detached HEAD.
//
// Everything is read from the `.git` entry directly: `sonar start` must not
// pay for a `git` subprocess on a path that runs before every dev server.
func GitContext(cwd string) (worktree, branch string) {
	root, linked, ok := groups.Find(cwd)
	if !ok {
		return "", ""
	}
	if linked != "" {
		worktree = filepath.Base(root)
	}
	return worktree, branchOf(root)
}

// Capture is what a spawn path calls: the detected session with the git
// context of the directory the command will run in filled in.
func Capture(cwd string, opts Options) (state.Session, bool) {
	s, ok := Detect(opts)
	if !ok {
		return state.Session{}, false
	}
	s.Worktree, s.Branch = GitContext(cwd)
	return s, true
}

// branchOf reads HEAD for a checkout root, following the `.git` file of a
// linked worktree or submodule to the directory that holds its own HEAD.
func branchOf(root string) string {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return ""
	}
	if info.Mode().IsRegular() {
		target := gitFileTarget(root, gitDir)
		if target == "" {
			return ""
		}
		gitDir = target
	}
	return parseHead(readTrimmed(filepath.Join(gitDir, "HEAD")))
}

// gitFileTarget resolves the `gitdir:` line of a `.git` file to an absolute
// path.
func gitFileTarget(root, gitFile string) string {
	for _, line := range strings.Split(readTrimmed(gitFile), "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !found {
			continue
		}
		target := strings.TrimSpace(rest)
		if target == "" {
			return ""
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		return filepath.Clean(target)
	}
	return ""
}

// parseHead turns a HEAD file into a branch name. A symbolic ref gives the
// branch; a detached HEAD gives the short commit, which is still what a person
// would call the thing they are on.
func parseHead(head string) string {
	if head == "" {
		return ""
	}
	if ref, found := strings.CutPrefix(head, "ref:"); found {
		ref = strings.TrimSpace(ref)
		if short, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return short
		}
		return ref
	}
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
