package claims_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raskrebs/sonar/internal/claims"
)

// TestIdentityFromACheckout covers the three shapes a working directory can
// have: a plain checkout, a linked worktree, and no repository at all.
func TestIdentityFromACheckout(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "myapp")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	project, worktree := claims.Identity(sub, "", "")
	if project != "myapp" || worktree != claims.DefaultWorktree {
		t.Errorf("Identity(checkout) = %q/%q, want myapp/%s", project, worktree, claims.DefaultWorktree)
	}

	// Explicit arguments win over the directory.
	if p, w := claims.Identity(sub, "other", "feature"); p != "other" || w != "feature" {
		t.Errorf("Identity with arguments = %q/%q, want other/feature", p, w)
	}
}

func TestIdentityOutsideARepository(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, worktree := claims.Identity(dir, "", "")
	if project != "loose" || worktree != claims.DefaultWorktree {
		t.Errorf("Identity(no repo) = %q/%q, want loose/%s", project, worktree, claims.DefaultWorktree)
	}
}

// A linked worktree keeps the repository as the project and the checkout as
// the worktree, so sibling worktrees of one repo get different keys.
func TestIdentityInALinkedWorktree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "myapp")
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "feature-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "myapp-feature-x")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	gitfile := "gitdir: " + filepath.Join(main, ".git", "worktrees", "feature-x") + "\n"
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte(gitfile), 0o644); err != nil {
		t.Fatal(err)
	}

	// groups.Find names a linked worktree "<repo>@<checkout dir>", so the
	// worktree half is the directory the agent is working in.
	project, worktree := claims.Identity(linked, "", "")
	if project != "myapp" || worktree != "myapp-feature-x" {
		t.Errorf("Identity(linked worktree) = %q/%q, want myapp/myapp-feature-x", project, worktree)
	}
	if key := claims.Key(project, worktree); key != "myapp/myapp-feature-x" {
		t.Errorf("key = %q, want myapp/myapp-feature-x", key)
	}
}
