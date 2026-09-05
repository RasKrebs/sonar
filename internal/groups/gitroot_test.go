package groups

import (
	"os"
	"path/filepath"
	"testing"
)

// tempTree returns a temp dir with symlinks resolved, because Find resolves
// them and macOS puts t.TempDir() behind /var -> /private/var.
func tempTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	base := tempTree(t)

	// A normal clone with a .git directory and a nested source dir.
	clone := mkdir(t, base, "clone")
	mkdir(t, clone, ".git")
	cloneDeep := mkdir(t, clone, "backend", "app")

	// A repository nested inside another repository.
	nested := mkdir(t, clone, "vendor", "inner")
	mkdir(t, nested, ".git")

	// A linked worktree: .git is a file pointing into the main repo's
	// worktrees/ directory.
	wt := mkdir(t, base, "worktrees", "feature-x")
	writeFile(t, filepath.Join(wt, ".git"),
		"gitdir: "+filepath.Join(clone, ".git", "worktrees", "feature-x")+"\n")
	wtDeep := mkdir(t, wt, "frontend")

	// A worktree whose .git file uses a relative gitdir.
	relwt := mkdir(t, base, "relwt")
	rel, err := filepath.Rel(relwt, filepath.Join(clone, ".git", "worktrees", "relwt"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(relwt, ".git"), "gitdir: "+rel+"\n")

	// A submodule: .git file into .git/modules, not a worktree.
	sub := mkdir(t, base, "sub")
	writeFile(t, filepath.Join(sub, ".git"),
		"gitdir: "+filepath.Join(clone, ".git", "modules", "sub")+"\n")

	// A .git file that says nothing useful.
	junk := mkdir(t, base, "junk")
	writeFile(t, filepath.Join(junk, ".git"), "not a gitdir line\n")

	outside := mkdir(t, base, "outside", "deep")

	tests := []struct {
		name      string
		cwd       string
		wantRoot  string
		wantWT    string
		wantOK    bool
		wantGroup string
	}{
		{"git dir at the root", clone, clone, "", true, "clone"},
		{"git dir from a nested cwd", cloneDeep, clone, "", true, "clone"},
		{"nested repo wins over its parent", nested, nested, "", true, "inner"},
		{"worktree is qualified", wt, wt, "clone@feature-x", true, "clone@feature-x"},
		{"worktree from a nested cwd", wtDeep, wt, "clone@feature-x", true, "clone@feature-x"},
		{"worktree with a relative gitdir", relwt, relwt, "clone@relwt", true, "clone@relwt"},
		{"submodule is not a worktree", sub, sub, "", true, "sub"},
		{"unparseable git file", junk, junk, "", true, "junk"},
		{"outside any repo", outside, "", "", false, ""},
		{"empty cwd", "", "", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, wt, ok := Find(tt.cwd)
			if root != tt.wantRoot || wt != tt.wantWT || ok != tt.wantOK {
				t.Fatalf("Find(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.cwd, root, wt, ok, tt.wantRoot, tt.wantWT, tt.wantOK)
			}
			if got := GroupName(root, wt); got != tt.wantGroup {
				t.Fatalf("GroupName(%q, %q) = %q, want %q", root, wt, got, tt.wantGroup)
			}
		})
	}
}
