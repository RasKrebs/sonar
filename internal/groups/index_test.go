package groups

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/state"
)

func TestIndexObserveWalksUpToTheGitRoot(t *testing.T) {
	base := tempTree(t)
	repo := mkdir(t, base, "repo")
	mkdir(t, repo, ".git")
	deep := mkdir(t, repo, "services", "api")
	writeFile(t, filepath.Join(repo, ConfigName), "name: repo\n")
	// A config above the repository must not be picked up.
	writeFile(t, filepath.Join(base, ConfigName), "name: outer\n")

	x := NewIndex()
	x.Observe(deep)

	if cfg := x.Nearest(deep); cfg == nil || cfg.Name != "repo" {
		t.Fatalf("Nearest(%q) = %+v, want the repo config", deep, cfg)
	}
	if x.At(base) != nil {
		t.Error("Observe walked past the git root")
	}
}

func TestIndexObserveOutsideARepoOnlyLooksAtTheDirectory(t *testing.T) {
	base := tempTree(t)
	dir := mkdir(t, base, "loose")
	writeFile(t, filepath.Join(dir, ConfigName), "name: loose\n")
	writeFile(t, filepath.Join(base, ConfigName), "name: outer\n")

	x := NewIndex()
	x.Observe(dir)
	if cfg := x.At(dir); cfg == nil || cfg.Name != "loose" {
		t.Fatalf("At(%q) = %+v", dir, cfg)
	}
	if x.At(base) != nil {
		t.Error("Observe walked up outside a repository")
	}
}

func TestIndexDeepestConfigWins(t *testing.T) {
	base := tempTree(t)
	repo := mkdir(t, base, "repo")
	mkdir(t, repo, ".git")
	inner := mkdir(t, repo, "frontend")
	writeFile(t, filepath.Join(repo, ConfigName), "name: outer\nservices:\n  - name: api\n    port: 8000\n")
	writeFile(t, filepath.Join(inner, ConfigName), "name: inner\nservices:\n  - name: web\n    port: 8000\n")

	x := NewIndex()
	x.Observe(inner)

	cfg, svc, ok := x.MatchPort(state.Port{Port: 8000, Cwd: inner})
	if !ok || cfg.Name != "inner" || svc != "web" {
		t.Fatalf("MatchPort = (%v, %q, %v), want the inner config", cfg, svc, ok)
	}
	cfg, svc, ok = x.MatchPort(state.Port{Port: 8000, Cwd: repo})
	if !ok || cfg.Name != "outer" || svc != "api" {
		t.Fatalf("MatchPort from the repo root = (%v, %q, %v)", cfg, svc, ok)
	}
	if _, _, ok := x.MatchPort(state.Port{Port: 8000}); ok {
		t.Error("a port with no cwd must not match a config")
	}
	if _, _, ok := x.MatchPort(state.Port{Port: 9999, Cwd: inner}); ok {
		t.Error("an unclaimed port must not match a config")
	}
}

func TestIndexRecordsInvalidConfigsWithoutFailing(t *testing.T) {
	base := tempTree(t)
	repo := mkdir(t, base, "repo")
	mkdir(t, repo, ".git")
	writeFile(t, filepath.Join(repo, ConfigName), "name: a b\n")

	x := NewIndex()
	x.Observe(repo)

	if got := x.Configs(); len(got) != 0 {
		t.Fatalf("Configs = %v, want none", got)
	}
	invalid := x.Invalid()
	if len(invalid) != 1 || !strings.Contains(invalid[0].Err.Error(), "whitespace") {
		t.Fatalf("Invalid = %+v", invalid)
	}
	// The resolver must fall back to the git root, not blow up.
	got := Resolve([]state.Port{nativePort(8123, repo)}, NoPins{}, NoRuns{}, x)[0]
	if deref(got.Group) != "repo" {
		t.Fatalf("group = %q, want the git root name", deref(got.Group))
	}
}

func TestIndexAcceptsTheYmlSpelling(t *testing.T) {
	base := tempTree(t)
	repo := mkdir(t, base, "repo")
	mkdir(t, repo, ".git")
	writeFile(t, filepath.Join(repo, ".sonar.yml"), "name: ymlrepo\n")

	x := NewIndex()
	x.Observe(repo)
	if cfg := x.At(repo); cfg == nil || cfg.Name != "ymlrepo" {
		t.Fatalf("At = %+v", cfg)
	}
}

// TestIndexKeysConfigsCanonically: a config reached through an unresolved path
// — a Reload root the store handed back, or a path a client typed — has to land
// under the same key as the same directory found by walking a process cwd. On
// macOS those two spellings differ for everything under $TMPDIR (/var against
// /private/var), and a mismatch makes the config invisible to Nearest, so
// `sonar start` falls back to the git root or the directory name.
func TestIndexKeysConfigsCanonically(t *testing.T) {
	real := tempTree(t)
	repo := mkdir(t, real, "repo")
	writeFile(t, filepath.Join(repo, ConfigName), "name: my-app\n")

	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	viaLink := filepath.Join(link, "repo")

	t.Run("AddFile", func(t *testing.T) {
		x := NewIndex()
		if err := x.AddFile(filepath.Join(viaLink, ConfigName)); err != nil {
			t.Fatal(err)
		}
		if cfg := x.Nearest(repo); cfg == nil || cfg.Name != "my-app" {
			t.Fatalf("Nearest(%s) = %+v after AddFile through a symlink", repo, cfg)
		}
		if cfg := x.At(viaLink); cfg == nil || cfg.Name != "my-app" {
			t.Fatalf("At(%s) = %+v", viaLink, cfg)
		}
	})

	t.Run("Reload", func(t *testing.T) {
		x := NewIndex()
		if n, bad := x.Reload([]string{viaLink}); n != 1 || len(bad) != 0 {
			t.Fatalf("Reload = %d, %v", n, bad)
		}
		if cfg := x.Nearest(filepath.Join(repo, "sub", "dir")); cfg == nil || cfg.Name != "my-app" {
			t.Fatalf("Nearest below the repo = %+v after Reload through a symlink", cfg)
		}
	})

	t.Run("ByPath", func(t *testing.T) {
		x := NewIndex()
		x.Observe(repo)
		if cfg, ok := x.ByPath(filepath.Join(viaLink, ConfigName)); !ok || cfg.Name != "my-app" {
			t.Fatalf("ByPath through a symlink = %+v, %v", cfg, ok)
		}
	})
}
