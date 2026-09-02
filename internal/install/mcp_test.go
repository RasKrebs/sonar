package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo makes a temp git root with a temp home, and returns options wired to
// them. Nothing in these tests touches the real filesystem or runs a real
// client CLI.
func newRepo(t *testing.T) (root string, home string, opts Options) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	home = t.TempDir()
	return root, home, Options{
		Client:  ClientClaudeCode,
		Scope:   ScopeProject,
		GitRoot: root,
		Home:    home,
		Binary:  "sonar",
		LookPath: func(string) (string, error) {
			return "", os.ErrNotExist
		},
		Run: func([]string) error {
			t.Fatal("Run must not be called for file-backed clients")
			return nil
		},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestInstallMCPCreatesFileFromNothing(t *testing.T) {
	root, _, opts := newRepo(t)
	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if res.Action != ActionCreated {
		t.Errorf("Action = %q, want %q", res.Action, ActionCreated)
	}
	path := filepath.Join(root, ".mcp.json")
	if res.Path != path {
		t.Errorf("Path = %q, want %q", res.Path, path)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	entry, ok := doc.MCPServers["sonar"]
	if !ok {
		t.Fatal("mcpServers.sonar missing")
	}
	if entry.Command != "sonar" || len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Errorf("entry = %+v, want command sonar args [mcp]", entry)
	}
}

func TestInstallMCPMergesPreservingOtherServersAndKeys(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")
	existing := "{\n  \"$schema\": \"https://example.com/schema.json\",\n" +
		"  \"mcpServers\": {\n" +
		"    \"other\": {\n      \"command\": \"other-server\",\n      \"args\": [\n        \"--flag\"\n      ],\n      \"env\": {\n        \"K\": \"v\"\n      }\n    }\n  },\n" +
		"  \"unrelatedTopLevel\": [\n    1,\n    2\n  ]\n}\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if res.Action != ActionUpdated {
		t.Errorf("Action = %q, want %q", res.Action, ActionUpdated)
	}

	got := readFile(t, path)
	obj, err := ParseObject([]byte(got))
	if err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if keys := obj.Keys(); !equalStrings(keys, []string{"$schema", "mcpServers", "unrelatedTopLevel"}) {
		t.Errorf("top level keys = %v, want original order preserved", keys)
	}
	servers, ok := obj.Object("mcpServers")
	if !ok {
		t.Fatal("mcpServers is not an object")
	}
	if keys := servers.Keys(); !equalStrings(keys, []string{"other", "sonar"}) {
		t.Errorf("server keys = %v, want [other sonar]", keys)
	}
	if !strings.Contains(got, `"command": "other-server"`) || !strings.Contains(got, `"K": "v"`) {
		t.Errorf("other server was not preserved:\n%s", got)
	}
	if !strings.Contains(got, `"unrelatedTopLevel"`) {
		t.Errorf("unrelated top level key was dropped:\n%s", got)
	}
}

func TestInstallMCPIsIdempotent(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")

	if _, err := InstallMCP(opts); err != nil {
		t.Fatalf("first InstallMCP: %v", err)
	}
	first := readFile(t, path)

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("second InstallMCP: %v", err)
	}
	if res.Action != ActionUnchanged {
		t.Errorf("second run Action = %q, want %q", res.Action, ActionUnchanged)
	}
	if second := readFile(t, path); second != first {
		t.Errorf("second run changed the file:\nfirst:  %q\nsecond: %q", first, second)
	}
}

func TestInstallMCPRefusesHandEditedEntryWithoutForce(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")
	existing := "{\n  \"mcpServers\": {\n    \"sonar\": {\n      \"command\": \"/opt/custom/sonar\",\n      \"args\": [\n        \"mcp\",\n        \"--verbose\"\n      ]\n    }\n  }\n}\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := InstallMCP(opts)
	if err == nil {
		t.Fatal("InstallMCP over a hand-edited entry = nil error, want error")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force, got: %v", err)
	}
	if got := readFile(t, path); got != existing {
		t.Errorf("refused install still wrote the file:\n%s", got)
	}

	opts.Force = true
	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP with --force: %v", err)
	}
	if res.Action != ActionUpdated {
		t.Errorf("forced Action = %q, want %q", res.Action, ActionUpdated)
	}
	if got := readFile(t, path); strings.Contains(got, "--verbose") {
		t.Errorf("--force did not replace the entry:\n%s", got)
	}
}

func TestInstallMCPRefusesUnparseableFile(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")
	broken := "{ this is not json"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMCP(opts); err == nil {
		t.Fatal("InstallMCP on unparseable file = nil error, want error")
	}
	if got := readFile(t, path); got != broken {
		t.Errorf("unparseable file was modified:\n%s", got)
	}
}

func TestInstallMCPWarnsAboutComments(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The file still has to parse; the warning is about what the rewrite loses.
	existing := "{\n  \"mcpServers\": {}\n}\n// trailing note\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Client = ClientCursor
	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "comment") {
		t.Errorf("Warnings = %v, want a comment warning", res.Warnings)
	}
}

func TestInstallMCPCursorScopes(t *testing.T) {
	root, home, opts := newRepo(t)
	opts.Client = ClientCursor

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("project scope: %v", err)
	}
	if want := filepath.Join(root, ".cursor", "mcp.json"); res.Path != want {
		t.Errorf("project Path = %q, want %q", res.Path, want)
	}

	opts.Scope = ScopeUser
	res, err = InstallMCP(opts)
	if err != nil {
		t.Fatalf("user scope: %v", err)
	}
	if want := filepath.Join(home, ".cursor", "mcp.json"); res.Path != want {
		t.Errorf("user Path = %q, want %q", res.Path, want)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("user scope file not created: %v", err)
	}
}
