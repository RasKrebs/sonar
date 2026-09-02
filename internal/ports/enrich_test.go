package ports

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindGitRoot(t *testing.T) {
	base := t.TempDir()

	dirRepo := filepath.Join(base, "dirrepo")
	if err := os.MkdirAll(filepath.Join(dirRepo, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dirRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	fileRepo := filepath.Join(base, "filerepo")
	if err := os.MkdirAll(filepath.Join(fileRepo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileRepo, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	none := filepath.Join(base, "none", "deep")
	if err := os.MkdirAll(none, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"git dir at root", dirRepo, dirRepo},
		{"git dir from nested cwd", filepath.Join(dirRepo, "a", "b"), dirRepo},
		{"git file (worktree)", filepath.Join(fileRepo, "sub"), fileRepo},
		{"no repo", none, ""},
		{"empty cwd", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findGitRoot(tt.cwd); got != tt.want {
				t.Fatalf("findGitRoot(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestDeriveGroupPrefersComposeProject(t *testing.T) {
	lp := ListeningPort{Cwd: "/tmp/x", DockerComposeProject: "storefront"}
	if got, src := deriveGroup(&lp, "/home/me/code/sonar"); got != "storefront" || src != "auto" {
		t.Fatalf("got %q/%q", got, src)
	}
	lp = ListeningPort{}
	if got, src := deriveGroup(&lp, "/home/me/code/sonar"); got != "sonar" || src != "auto" {
		t.Fatalf("got %q/%q", got, src)
	}
	lp = ListeningPort{}
	if got, src := deriveGroup(&lp, ""); got != "" || src != "" {
		t.Fatalf("got %q/%q", got, src)
	}
}

func TestParseStartTime(t *testing.T) {
	if got := parseStartTime("Wed Mar 18 10:30:00 2026"); got != "2026-03-18T10:30:00Z" {
		// ps prints local time; compare only the date/time prefix.
		if len(got) < 19 || got[:19] != "2026-03-18T10:30:00" {
			t.Fatalf("parseStartTime(lstart) = %q", got)
		}
	}
	if got := parseStartTime("2026-09-02T09:12:41+02:00"); got != "2026-09-02T09:12:41+02:00" {
		t.Fatalf("parseStartTime(rfc3339) = %q", got)
	}
	if got := parseStartTime("nonsense"); got != "" {
		t.Fatalf("parseStartTime(garbage) = %q", got)
	}
}
