package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseClient(t *testing.T) {
	cases := map[string]Client{
		"claude-code": ClientClaudeCode,
		"cursor":      ClientCursor,
		"codex":       ClientCodex,
		"generic":     ClientGeneric,
	}
	for in, want := range cases {
		got, err := ParseClient(in)
		if err != nil {
			t.Fatalf("ParseClient(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseClient(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseClient("vscode"); err == nil {
		t.Error("ParseClient(vscode) = nil error, want error")
	}
}

func TestParseScope(t *testing.T) {
	if s, err := ParseScope("project"); err != nil || s != ScopeProject {
		t.Errorf("ParseScope(project) = %q, %v", s, err)
	}
	if s, err := ParseScope("user"); err != nil || s != ScopeUser {
		t.Errorf("ParseScope(user) = %q, %v", s, err)
	}
	if s, err := ParseScope(""); err != nil || s != ScopeProject {
		t.Errorf("ParseScope(\"\") = %q, %v, want project", s, err)
	}
	if _, err := ParseScope("global"); err == nil {
		t.Error("ParseScope(global) = nil error, want error")
	}
}

func TestResolveTargetFileClients(t *testing.T) {
	root := "/repo"
	home := "/home/dev"
	cases := []struct {
		name     string
		client   Client
		scope    Scope
		wantPath string
	}{
		{"claude project", ClientClaudeCode, ScopeProject, filepath.Join(root, ".mcp.json")},
		{"cursor project", ClientCursor, ScopeProject, filepath.Join(root, ".cursor", "mcp.json")},
		{"cursor user", ClientCursor, ScopeUser, filepath.Join(home, ".cursor", "mcp.json")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, err := ResolveTarget(tc.client, tc.scope, root, home)
			if err != nil {
				t.Fatalf("ResolveTarget: %v", err)
			}
			if target.Kind != TargetFile {
				t.Fatalf("Kind = %q, want file", target.Kind)
			}
			if target.Path != tc.wantPath {
				t.Errorf("Path = %q, want %q", target.Path, tc.wantPath)
			}
		})
	}
}

func TestResolveTargetCommandClients(t *testing.T) {
	claude, err := ResolveTarget(ClientClaudeCode, ScopeUser, "/repo", "/home/dev")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if claude.Kind != TargetCommand || claude.Tool != "claude" {
		t.Fatalf("claude user target = %+v", claude)
	}
	codex, err := ResolveTarget(ClientCodex, ScopeUser, "/repo", "/home/dev")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if codex.Kind != TargetCommand || codex.Tool != "codex" {
		t.Fatalf("codex user target = %+v", codex)
	}
	generic, err := ResolveTarget(ClientGeneric, ScopeProject, "/repo", "/home/dev")
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if generic.Kind != TargetSnippet {
		t.Fatalf("generic target = %+v", generic)
	}
}

func TestResolveTargetRejectsCodexProjectScope(t *testing.T) {
	if _, err := ResolveTarget(ClientCodex, ScopeProject, "/repo", "/home/dev"); err == nil {
		t.Error("codex project scope = nil error, want error")
	}
}

func TestFindGitRootWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindGitRoot(deep)
	if err != nil {
		t.Fatalf("FindGitRoot: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantRoot {
		t.Errorf("FindGitRoot = %q, want %q", got, wantRoot)
	}
}

func TestFindGitRootAcceptsGitFileForWorktrees(t *testing.T) {
	root := t.TempDir()
	// A linked worktree has a .git *file*, not a directory.
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindGitRoot(root)
	if err != nil {
		t.Fatalf("FindGitRoot: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantRoot {
		t.Errorf("FindGitRoot = %q, want %q", got, wantRoot)
	}
}

func TestFindGitRootErrorsOutsideRepo(t *testing.T) {
	if _, err := FindGitRoot(t.TempDir()); err == nil {
		t.Error("FindGitRoot outside a repo = nil error, want error")
	}
}

func TestResolveBinaryPrefersBareNameWhenOnPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sonar")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := resolveBinary(exe, func(string) (string, error) { return exe, nil })
	if got != "sonar" {
		t.Errorf("resolveBinary = %q, want \"sonar\"", got)
	}
}

func TestResolveBinaryFallsBackToAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sonar")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "sonar")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// resolveBinary resolves symlinks before comparing, and macOS temp dirs
	// live behind /var -> /private/var, so compare against the resolved path.
	wantExe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveBinary(exe, func(string) (string, error) { return other, nil }); got != wantExe {
		t.Errorf("resolveBinary with different PATH entry = %q, want %q", got, wantExe)
	}
	notFound := func(string) (string, error) { return "", os.ErrNotExist }
	if got := resolveBinary(exe, notFound); got != wantExe {
		t.Errorf("resolveBinary with sonar off PATH = %q, want %q", got, wantExe)
	}
}
