package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo makes a real checkout with one commit. Everything here reads .git
// directly, so the fixture has to be a real repository rather than a fake.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main", ".")
	run("commit", "-q", "--allow-empty", "-m", "first")
}

func TestGitContextMainCheckoutHasNoWorktree(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)

	worktree, branch := GitContext(root)
	if worktree != "" {
		t.Errorf("worktree = %q, want empty for the primary checkout", worktree)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestGitContextFromASubdirectory(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, branch := GitContext(sub)
	if branch != "main" {
		t.Errorf("branch from a subdirectory = %q, want main", branch)
	}
}

func TestGitContextNamesALinkedWorktree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	gitRepo(t, root)

	linked := filepath.Join(base, "feature-x")
	cmd := exec.Command("git", "worktree", "add", "-q", "-b", "feature/x", linked)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree add: %v\n%s", err, out)
	}

	worktree, branch := GitContext(linked)
	if worktree != "feature-x" {
		t.Errorf("worktree = %q, want feature-x", worktree)
	}
	if branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", branch)
	}

	// The primary checkout of the same repository still reports no worktree.
	if w, _ := GitContext(root); w != "" {
		t.Errorf("primary checkout reported worktree %q", w)
	}
}

func TestGitContextOutsideARepository(t *testing.T) {
	worktree, branch := GitContext(t.TempDir())
	if worktree != "" || branch != "" {
		t.Errorf("GitContext outside a repo = %q, %q", worktree, branch)
	}
}

func TestCaptureFillsTheGitContext(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)

	got, ok := Capture(root, Options{Getenv: env(map[string]string{EnvSession: "claude-code:s1"})})
	if !ok {
		t.Fatal("Capture found no session")
	}
	if got.ID != "s1" || got.Branch != "main" {
		t.Errorf("Capture = %+v", got)
	}

	if _, ok := Capture(root, Options{Getenv: env(nil), Processes: func() []Process { return nil }}); ok {
		t.Error("Capture invented a session with no agent in the environment")
	}
}

func TestParseHead(t *testing.T) {
	for _, tc := range []struct{ head, want string }{
		{"ref: refs/heads/main", "main"},
		{"ref: refs/heads/feature/x", "feature/x"},
		{"ref: refs/remotes/origin/main", "refs/remotes/origin/main"},
		{"0123456789abcdef0123456789abcdef01234567", "0123456789ab"},
		{"", ""},
	} {
		if got := parseHead(tc.head); got != tc.want {
			t.Errorf("parseHead(%q) = %q, want %q", tc.head, got, tc.want)
		}
	}
}
