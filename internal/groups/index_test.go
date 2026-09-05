package groups

import (
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
