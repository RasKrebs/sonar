package install

import (
	"encoding/json"
	"errors"
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

func TestInstallMCPUninstallRemovesOnlySonar(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")
	existing := "{\n  \"mcpServers\": {\n    \"other\": {\n      \"command\": \"other-server\"\n    }\n  },\n  \"keepMe\": true\n}\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMCP(opts); err != nil {
		t.Fatalf("install: %v", err)
	}

	opts.Uninstall = true
	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if res.Action != ActionRemoved {
		t.Errorf("Action = %q, want %q", res.Action, ActionRemoved)
	}

	got := readFile(t, path)
	if strings.Contains(got, `"sonar"`) {
		t.Errorf("sonar entry survived uninstall:\n%s", got)
	}
	if !strings.Contains(got, `"other-server"`) || !strings.Contains(got, `"keepMe"`) {
		t.Errorf("uninstall removed more than sonar's entry:\n%s", got)
	}
	obj, err := ParseObject([]byte(got))
	if err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	servers, _ := obj.Object("mcpServers")
	if keys := servers.Keys(); !equalStrings(keys, []string{"other"}) {
		t.Errorf("server keys = %v, want [other]", keys)
	}
}

func TestInstallMCPUninstallIsIdempotentAndAbsentIsNoError(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")
	opts.Uninstall = true

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("uninstall with no file: %v", err)
	}
	if res.Action != ActionAbsent {
		t.Errorf("Action = %q, want %q", res.Action, ActionAbsent)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("uninstall created a file that did not exist")
	}

	opts.Uninstall = false
	if _, err := InstallMCP(opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	opts.Uninstall = true
	if _, err := InstallMCP(opts); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	after := readFile(t, path)
	res, err = InstallMCP(opts)
	if err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if res.Action != ActionAbsent {
		t.Errorf("second uninstall Action = %q, want %q", res.Action, ActionAbsent)
	}
	if again := readFile(t, path); again != after {
		t.Errorf("second uninstall changed the file:\n%q\n%q", after, again)
	}
}

func TestInstallMCPPrintWritesNothingToDisk(t *testing.T) {
	root, _, opts := newRepo(t)
	path := filepath.Join(root, ".mcp.json")
	opts.Print = true

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if res.Action != ActionPrinted {
		t.Errorf("Action = %q, want %q", res.Action, ActionPrinted)
	}
	if !strings.Contains(res.Output, `"mcpServers"`) || !strings.Contains(res.Output, `"sonar"`) {
		t.Errorf("Output does not contain the entry:\n%s", res.Output)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("--print created %s", path)
	}

	// --print over an existing file leaves it byte-identical.
	existing := "{\n  \"mcpServers\": {\n    \"other\": {\n      \"command\": \"other-server\"\n    }\n  }\n}\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallMCP(opts); err != nil {
		t.Fatalf("InstallMCP over existing: %v", err)
	}
	if got := readFile(t, path); got != existing {
		t.Errorf("--print modified an existing file:\n%s", got)
	}
}

func TestInstallMCPGenericPrintsSnippet(t *testing.T) {
	_, _, opts := newRepo(t)
	opts.Client = ClientGeneric
	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if res.Action != ActionPrinted {
		t.Errorf("Action = %q, want %q", res.Action, ActionPrinted)
	}
	var doc map[string]map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(res.Output), &doc); err != nil {
		t.Fatalf("snippet is not valid JSON: %v\n%s", err, res.Output)
	}
	if doc["mcpServers"]["sonar"].Command != "sonar" {
		t.Errorf("snippet = %s", res.Output)
	}
}

func TestInstallMCPClaudeUserScopeRunsClaudeCLI(t *testing.T) {
	_, _, opts := newRepo(t)
	opts.Scope = ScopeUser
	opts.Binary = "/opt/bin/sonar"
	var ran []string
	opts.LookPath = func(string) (string, error) { return "/usr/local/bin/claude", nil }
	opts.Run = func(argv []string) error { ran = argv; return nil }

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if res.Action != ActionRan {
		t.Errorf("Action = %q, want %q", res.Action, ActionRan)
	}
	want := []string{"claude", "mcp", "add", "--scope", "user", "sonar", "--", "/opt/bin/sonar", "mcp"}
	if !equalStrings(ran, want) {
		t.Errorf("ran %v, want %v", ran, want)
	}
}

func TestInstallMCPClaudeUserScopeMissingCLIErrorsWithCommand(t *testing.T) {
	_, _, opts := newRepo(t)
	opts.Scope = ScopeUser
	opts.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	opts.Run = func([]string) error {
		t.Fatal("Run must not be called when the CLI is missing")
		return nil
	}

	res, err := InstallMCP(opts)
	if !errors.Is(err, ErrClientCLIMissing) {
		t.Fatalf("err = %v, want ErrClientCLIMissing", err)
	}
	if !strings.Contains(res.Output, "claude mcp add --scope user sonar -- sonar mcp") {
		t.Errorf("Output should show the command to run, got:\n%s", res.Output)
	}
}

func TestInstallMCPCodexMissingCLIPrintsTOML(t *testing.T) {
	_, _, opts := newRepo(t)
	opts.Client = ClientCodex
	opts.Scope = ScopeUser
	opts.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	opts.Run = func([]string) error {
		t.Fatal("Run must not be called when the CLI is missing")
		return nil
	}

	res, err := InstallMCP(opts)
	if !errors.Is(err, ErrClientCLIMissing) {
		t.Fatalf("err = %v, want ErrClientCLIMissing", err)
	}
	if !strings.Contains(res.Output, "[mcp_servers.sonar]") {
		t.Errorf("Output should carry the TOML block, got:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, `command = "sonar"`) {
		t.Errorf("TOML block missing command line:\n%s", res.Output)
	}
}

func TestInstallMCPCommandClientPrintDoesNotRun(t *testing.T) {
	_, _, opts := newRepo(t)
	opts.Client = ClientCodex
	opts.Scope = ScopeUser
	opts.Print = true
	opts.LookPath = func(string) (string, error) {
		t.Fatal("LookPath must not be called for --print")
		return "", nil
	}
	opts.Run = func([]string) error {
		t.Fatal("Run must not be called for --print")
		return nil
	}

	res, err := InstallMCP(opts)
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if got := strings.TrimSpace(res.Output); got != "codex mcp add sonar -- sonar mcp" {
		t.Errorf("Output = %q", got)
	}
}
