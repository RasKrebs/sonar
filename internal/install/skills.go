package install

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkillVersion is bumped whenever the bundled skill changes meaningfully; it
// appears in the managed marker so a stale install can be recognised.
const SkillVersion = 1

//go:embed skill/SKILL.md
var skillMarkdown string

// SkillContent is the exact bytes `sonar install skills` writes.
func SkillContent() string { return skillMarkdown }

// markerPrefix matches any version of the managed marker.
const markerPrefix = "<!-- managed by sonar; version "

// managedMarker is the line that says sonar owns this file. It sits on the
// first line after the YAML frontmatter, not on line 1: a SKILL.md whose first
// line is not `---` does not parse as a skill, so the marker cannot go there.
func managedMarker() string {
	return fmt.Sprintf("%s%d -->", markerPrefix, SkillVersion)
}

// markerScanLines bounds how far into a file the marker is looked for, so a
// user's prose mentioning it further down cannot fake ownership.
const markerScanLines = 20

// IsManaged reports whether content was written by sonar.
func IsManaged(content string) bool {
	for i, l := range strings.SplitN(content, "\n", markerScanLines+1) {
		if i >= markerScanLines {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(l), markerPrefix) {
			return true
		}
	}
	return false
}

// InstallSkill writes the bundled skill to path. An existing file carrying the
// managed marker is overwritten; one without it is left alone unless force.
func InstallSkill(path string, force bool) (Action, error) {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if string(existing) == SkillContent() {
			return ActionUnchanged, nil
		}
		if !force && !IsManaged(string(existing)) {
			return ActionSkipped, fmt.Errorf("%s exists and was not written by sonar (use --force to overwrite)", path)
		}
	case !os.IsNotExist(err):
		return ActionSkipped, fmt.Errorf("could not read %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ActionSkipped, fmt.Errorf("could not create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(SkillContent()), 0o644); err != nil {
		return ActionSkipped, fmt.Errorf("could not write %s: %w", path, err)
	}
	return ActionWrote, nil
}

// UninstallSkill removes the skill directory, but only if the file carries the
// managed marker.
func UninstallSkill(path string) (Action, error) {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ActionAbsent, nil
	}
	if err != nil {
		return ActionSkipped, fmt.Errorf("could not read %s: %w", path, err)
	}
	if !IsManaged(string(existing)) {
		return ActionSkipped, fmt.Errorf("%s was not written by sonar; leaving it alone", path)
	}
	dir := filepath.Dir(path)
	if filepath.Base(dir) != SkillName {
		// Defensive: never recursively remove a directory sonar did not name.
		if err := os.Remove(path); err != nil {
			return ActionSkipped, fmt.Errorf("could not remove %s: %w", path, err)
		}
		return ActionRemoved, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return ActionSkipped, fmt.Errorf("could not remove %s: %w", dir, err)
	}
	return ActionRemoved, nil
}
