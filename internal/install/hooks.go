package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SonarTag marks a hook entry as written by sonar, so uninstall removes
// exactly those entries and leaves the user's own hooks alone.
const SonarTag = "_sonar"

// Mode selects how the pre-bash hook reacts to a bare dev-server command.
type Mode string

const (
	// ModeAdvise returns additionalContext and allows the call.
	ModeAdvise Mode = "advise"
	// ModeStrict (blocking, exit code 2) is deferred to a later slice.
	ModeStrict Mode = "strict"
)

// ParseMode validates a --mode flag value.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAdvise:
		return ModeAdvise, nil
	case ModeStrict:
		return "", fmt.Errorf("--mode strict is not implemented yet; use --mode advise")
	default:
		return "", fmt.Errorf("invalid mode %q: use advise", s)
	}
}

// hookGroups builds the two entries sonar owns, in Claude Code's settings
// schema: hooks.<Event> is a list of groups, each with an optional matcher and
// a list of command entries.
func hookGroups(bin string, mode Mode) map[string]any {
	_ = mode // only advise exists today; strict will add a flag to the command
	return map[string]any{
		"SessionStart": map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": bin + " hook session-start",
					SonarTag:  true,
				},
			},
		},
		"PreToolUse": map[string]any{
			"matcher": "Bash",
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": bin + " hook pre-bash",
					SonarTag:  true,
				},
			},
		},
	}
}

// InstallHooks merges sonar's hooks into a Claude Code settings file,
// preserving every other key. It is idempotent: sonar's own entries are
// stripped first, so running it twice yields the same file.
func InstallHooks(path, bin string, mode Mode) (Action, []string, error) {
	before, root, warnings, err := loadSettings(path)
	if err != nil {
		return ActionSkipped, warnings, err
	}

	stripSonarHooks(root)

	hooksVal, _ := root["hooks"].(map[string]any)
	if hooksVal == nil {
		hooksVal = map[string]any{}
	}
	for event, group := range hookGroups(bin, mode) {
		list, _ := hooksVal[event].([]any)
		hooksVal[event] = append(list, group)
	}
	root["hooks"] = hooksVal

	after, err := marshalSettings(root)
	if err != nil {
		return ActionSkipped, warnings, err
	}
	if bytes.Equal(before, after) {
		return ActionUnchanged, warnings, nil
	}
	if err := writeSettings(path, after); err != nil {
		return ActionSkipped, warnings, err
	}
	return ActionWrote, warnings, nil
}

// UninstallHooks removes every entry tagged _sonar and any scaffolding left
// empty by their removal.
func UninstallHooks(path string) (Action, error) {
	before, root, _, err := loadSettings(path)
	if err != nil {
		return ActionSkipped, err
	}
	if before == nil {
		return ActionAbsent, nil
	}
	stripSonarHooks(root)
	after, err := marshalSettings(root)
	if err != nil {
		return ActionSkipped, err
	}
	if bytes.Equal(before, after) {
		return ActionAbsent, nil
	}
	if err := writeSettings(path, after); err != nil {
		return ActionSkipped, err
	}
	return ActionRemoved, nil
}

// loadSettings returns the canonical bytes of the existing file (nil when the
// file does not exist) and its decoded form.
func loadSettings(path string) ([]byte, map[string]any, []string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, map[string]any{}, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("could not read %s: %w", path, err)
	}
	var warnings []string
	if bytes.Contains(data, []byte("//")) || bytes.Contains(data, []byte("/*")) {
		warnings = append(warnings, fmt.Sprintf("%s appears to contain comments; sonar edits it as strict JSON and cannot preserve them", path))
	}
	root := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, nil, warnings, fmt.Errorf("could not parse %s as JSON: %w", path, err)
		}
	}
	canonical, err := marshalSettings(root)
	if err != nil {
		return nil, nil, warnings, err
	}
	return canonical, root, warnings, nil
}

func marshalSettings(root map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("could not encode settings: %w", err)
	}
	return append(out, '\n'), nil
}

func writeSettings(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	return nil
}

// stripSonarHooks removes every _sonar-tagged entry and prunes the containers
// that removal leaves empty. The user's own hooks are untouched.
func stripSonarHooks(root map[string]any) {
	hooksVal, ok := root["hooks"].(map[string]any)
	if !ok {
		return
	}
	for event, groupsVal := range hooksVal {
		groups, ok := groupsVal.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(groups))
		for _, gv := range groups {
			g, ok := gv.(map[string]any)
			if !ok {
				kept = append(kept, gv)
				continue
			}
			if tagged(g) {
				continue
			}
			entries, ok := g["hooks"].([]any)
			if !ok {
				kept = append(kept, gv)
				continue
			}
			keptEntries := make([]any, 0, len(entries))
			for _, ev := range entries {
				if e, ok := ev.(map[string]any); ok && tagged(e) {
					continue
				}
				keptEntries = append(keptEntries, ev)
			}
			if len(keptEntries) == 0 {
				continue
			}
			g["hooks"] = keptEntries
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooksVal, event)
			continue
		}
		hooksVal[event] = kept
	}
	if len(hooksVal) == 0 {
		delete(root, "hooks")
		return
	}
	root["hooks"] = hooksVal
}

func tagged(m map[string]any) bool {
	b, _ := m[SonarTag].(bool)
	return b
}

// HookFragment renders just the hooks sonar would merge, for --print.
func HookFragment(bin string, mode Mode) (string, error) {
	target := map[string]any{}
	for event, group := range hookGroups(bin, mode) {
		target[event] = []any{group}
	}
	out, err := json.MarshalIndent(map[string]any{"hooks": target}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("could not encode hooks: %w", err)
	}
	return string(out), nil
}

// SonarBinary is the command string to put in a hook entry: the bare name when
// the running executable is what `sonar` resolves to on PATH, otherwise its
// absolute path.
func SonarBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return BinaryName
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if onPath(exe) {
		return BinaryName
	}
	return exe
}

func onPath(exe string) bool {
	found, err := exec.LookPath(BinaryName)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}
	return strings.EqualFold(found, exe)
}
