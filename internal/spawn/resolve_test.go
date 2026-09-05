package spawn

import (
	"os"
	"path/filepath"
	"testing"
)

// tempRepo builds a directory tree under a resolved temp dir. entries maps a
// relative path to its content; a path ending in "/" is a directory.
func tempRepo(t *testing.T, name string, entries map[string]string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range entries {
		path := filepath.Join(root, rel)
		if content == "" && filepath.Ext(rel) == "" {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveGroupAndName(t *testing.T) {
	gitRepo := tempRepo(t, "my-repo", map[string]string{".git": ""})

	configured := tempRepo(t, "checkout", map[string]string{
		".git": "",
		".sonar.yaml": "name: sonar\n" +
			"services:\n" +
			"  - name: api\n" +
			"    cmd: uv run uvicorn app:app\n" +
			"    port: 8000\n" +
			"  - name: web\n" +
			"    cmd: npm run dev\n" +
			"    cwd: frontend\n" +
			"    port: 5173\n",
		"frontend/.keep": "x",
	})

	plain := tempRepo(t, "just-a-dir", nil)

	cases := []struct {
		desc      string
		cwd       string
		argv      []string
		groupFlag string
		nameFlag  string
		wantGroup string
		wantName  string
	}{
		{
			desc: "git root names the group, argv names the service",
			cwd:  gitRepo, argv: []string{"npm", "run", "dev"},
			wantGroup: "my-repo", wantName: "dev",
		},
		{
			desc: "a nested directory still resolves to the checkout",
			cwd:  gitRepo, argv: []string{"./dev.sh"},
			wantGroup: "my-repo", wantName: "dev.sh",
		},
		{
			desc: ".sonar.yaml wins over the directory name",
			cwd:  configured, argv: []string{"uv", "run", "uvicorn", "app:app"},
			wantGroup: "sonar", wantName: "api",
		},
		{
			desc: "a service with a cwd only matches when started there",
			cwd:  configured, argv: []string{"npm", "run", "dev"},
			wantGroup: "sonar", wantName: "dev",
		},
		{
			desc: "the service's own cwd matches it",
			cwd:  filepath.Join(configured, "frontend"), argv: []string{"npm", "run", "dev"},
			wantGroup: "sonar", wantName: "web",
		},
		{
			desc: "no config and no checkout falls back to the directory name",
			cwd:  plain, argv: []string{"python3", "-m", "http.server"},
			wantGroup: "just-a-dir", wantName: "http.server",
		},
		{
			desc: "flags win over everything",
			cwd:  configured, argv: []string{"uv", "run", "uvicorn", "app:app"},
			groupFlag: "itest", nameFlag: "web",
			wantGroup: "itest", wantName: "web",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := Resolve(tc.cwd, tc.argv, tc.groupFlag, tc.nameFlag)
			if got.Group != tc.wantGroup || got.Name != tc.wantName {
				t.Fatalf("Resolve(%s, %q) = %q/%q, want %q/%q",
					tc.cwd, tc.argv, got.Group, got.Name, tc.wantGroup, tc.wantName)
			}
		})
	}
}

func TestResolveWorktreeGroupName(t *testing.T) {
	main := tempRepo(t, "sonar", map[string]string{".git/worktrees/feature/.keep": "x"})
	wt := tempRepo(t, "feature", map[string]string{
		".git": "gitdir: " + filepath.Join(main, ".git", "worktrees", "feature") + "\n",
	})
	got := Resolve(wt, []string{"npm", "run", "dev"}, "", "")
	if got.Group != "sonar@feature" {
		t.Fatalf("worktree group = %q, want sonar@feature", got.Group)
	}
}
