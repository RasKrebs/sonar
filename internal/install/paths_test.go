package install

import (
	"path/filepath"
	"testing"
)

func TestSkillPathUser(t *testing.T) {
	got := SkillPath(ScopeUser, "/home/u", "/repo")
	want := filepath.Join("/home/u", ".claude", "skills", "sonar", "SKILL.md")
	if got != want {
		t.Errorf("SkillPath(user) = %q, want %q", got, want)
	}
}

func TestSkillPathProject(t *testing.T) {
	got := SkillPath(ScopeProject, "/home/u", "/repo")
	want := filepath.Join("/repo", ".claude", "skills", "sonar", "SKILL.md")
	if got != want {
		t.Errorf("SkillPath(project) = %q, want %q", got, want)
	}
}

func TestSettingsPath(t *testing.T) {
	if got, want := SettingsPath(ScopeUser, "/home/u", "/repo"), filepath.Join("/home/u", ".claude", "settings.json"); got != want {
		t.Errorf("SettingsPath(user) = %q, want %q", got, want)
	}
	if got, want := SettingsPath(ScopeProject, "/home/u", "/repo"), filepath.Join("/repo", ".claude", "settings.json"); got != want {
		t.Errorf("SettingsPath(project) = %q, want %q", got, want)
	}
}
