package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readSettingsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("settings is not valid JSON: %v\n%s", err, data)
	}
	return m
}

// sonarCommands walks the hooks tree and returns the commands of every entry
// tagged _sonar, keyed by event name.
func sonarCommands(t *testing.T, m map[string]any) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	hooksVal, _ := m["hooks"].(map[string]any)
	for event, groupsVal := range hooksVal {
		groups, _ := groupsVal.([]any)
		for _, gv := range groups {
			g, _ := gv.(map[string]any)
			entries, _ := g["hooks"].([]any)
			for _, ev := range entries {
				e, _ := ev.(map[string]any)
				if tag, _ := e[SonarTag].(bool); tag {
					cmd, _ := e["command"].(string)
					out[event] = append(out[event], cmd)
				}
			}
		}
	}
	return out
}

func TestInstallHooksCreatesSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	act, _, err := InstallHooks(path, "sonar", ModeAdvise)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionWrote {
		t.Errorf("action = %q, want %q", act, ActionWrote)
	}
	got := sonarCommands(t, readSettingsFile(t, path))
	if len(got["SessionStart"]) != 1 || got["SessionStart"][0] != "sonar hook session-start" {
		t.Errorf("SessionStart = %q", got["SessionStart"])
	}
	if len(got["PreToolUse"]) != 1 || got["PreToolUse"][0] != "sonar hook pre-bash" {
		t.Errorf("PreToolUse = %q", got["PreToolUse"])
	}
}

func TestInstallHooksSetsBashMatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, _, err := InstallHooks(path, "sonar", ModeAdvise); err != nil {
		t.Fatal(err)
	}
	m := readSettingsFile(t, path)
	groups := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	g := groups[0].(map[string]any)
	if g["matcher"] != "Bash" {
		t.Errorf("matcher = %v, want Bash", g["matcher"])
	}
}

func TestInstallHooksUsesAbsoluteBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, _, err := InstallHooks(path, "/opt/bin/sonar", ModeAdvise); err != nil {
		t.Fatal(err)
	}
	got := sonarCommands(t, readSettingsFile(t, path))
	if got["SessionStart"][0] != "/opt/bin/sonar hook session-start" {
		t.Errorf("SessionStart = %q", got["SessionStart"])
	}
}

func TestInstallHooksIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, _, err := InstallHooks(path, "sonar", ModeAdvise); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	act, _, err := InstallHooks(path, "sonar", ModeAdvise)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionUnchanged {
		t.Errorf("second install action = %q, want %q", act, ActionUnchanged)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("reinstall changed the file:\n%s\n---\n%s", first, second)
	}
	got := sonarCommands(t, readSettingsFile(t, path))
	if len(got["SessionStart"]) != 1 || len(got["PreToolUse"]) != 1 {
		t.Errorf("reinstall duplicated entries: %v", got)
	}
}

func TestInstallHooksPreservesForeignContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "notify-send done"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallHooks(path, "sonar", ModeAdvise); err != nil {
		t.Fatal(err)
	}
	m := readSettingsFile(t, path)
	if _, ok := m["permissions"]; !ok {
		t.Error("permissions key was dropped")
	}
	hooksVal := m["hooks"].(map[string]any)
	if _, ok := hooksVal["Stop"]; !ok {
		t.Error("Stop hooks were dropped")
	}
	pre := hooksVal["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Fatalf("PreToolUse groups = %d, want 2 (the user's plus sonar's)", len(pre))
	}
	first := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if first["command"] != "my-linter" {
		t.Errorf("user's hook was displaced: %v", first)
	}
}

func TestUninstallHooksRemovesOnlySonarEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "permissions": {"allow": []},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallHooks(path, "sonar", ModeAdvise); err != nil {
		t.Fatal(err)
	}
	act, err := UninstallHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionRemoved {
		t.Errorf("action = %q, want %q", act, ActionRemoved)
	}
	m := readSettingsFile(t, path)
	if got := sonarCommands(t, m); len(got) != 0 {
		t.Errorf("sonar entries survived uninstall: %v", got)
	}
	pre := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("PreToolUse groups = %d, want 1", len(pre))
	}
	entry := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if entry["command"] != "my-linter" {
		t.Errorf("user's hook was removed: %v", entry)
	}
	if _, ok := m["permissions"]; !ok {
		t.Error("permissions key was dropped")
	}
}

func TestUninstallHooksLeavesNoEmptyScaffolding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, _, err := InstallHooks(path, "sonar", ModeAdvise); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallHooks(path); err != nil {
		t.Fatal(err)
	}
	m := readSettingsFile(t, path)
	if _, ok := m["hooks"]; ok {
		t.Errorf("empty hooks key should have been removed: %v", m)
	}
}

func TestUninstallHooksOnMissingFile(t *testing.T) {
	act, err := UninstallHooks(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionAbsent {
		t.Errorf("action = %q, want %q", act, ActionAbsent)
	}
}

func TestInstallHooksRefusesCommentedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := "{\n  // keep ls allowed\n  \"permissions\": {}\n}"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := InstallHooks(path, "sonar", ModeAdvise)
	if err == nil {
		t.Error("expected an error: the file is not valid JSON")
	}
	if len(warnings) == 0 {
		t.Error("expected a warning that comments are not supported")
	}
	got, _ := os.ReadFile(path)
	if string(got) != body {
		t.Error("a file sonar could not parse must be left byte-identical")
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode("advise"); err != nil || m != ModeAdvise {
		t.Errorf("ParseMode(advise) = %v, %v", m, err)
	}
	if _, err := ParseMode("strict"); err == nil {
		t.Error("strict mode is deferred and must be refused")
	}
	if _, err := ParseMode("loud"); err == nil {
		t.Error("unknown mode must be refused")
	}
}
