package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillContentIsAValidSkill(t *testing.T) {
	c := SkillContent()
	if !strings.HasPrefix(c, "---\n") {
		t.Fatal("SKILL.md must start with YAML frontmatter on line 1")
	}
	end := strings.Index(c[4:], "\n---\n")
	if end < 0 {
		t.Fatal("SKILL.md frontmatter is not closed")
	}
	fm := c[4 : 4+end]
	if !strings.Contains(fm, "name: "+SkillName) {
		t.Errorf("frontmatter missing name: %s, got:\n%s", SkillName, fm)
	}
	if !strings.Contains(fm, "description:") {
		t.Errorf("frontmatter missing description, got:\n%s", fm)
	}
	if !strings.Contains(fm, "Use when") && !strings.Contains(fm, "Use whenever") {
		t.Errorf("description must carry triggers, got:\n%s", fm)
	}
	if !IsManaged(c) {
		t.Error("bundled SKILL.md must carry the managed marker")
	}
	if !strings.Contains(c, managedMarker()) {
		t.Errorf("bundled marker must match version %d", SkillVersion)
	}
	if !strings.Contains(c, "sonar start --") {
		t.Error("skill must teach `sonar start --`")
	}
}

func TestInstallSkillWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "skills", "sonar", "SKILL.md")
	act, err := InstallSkill(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionWrote {
		t.Errorf("action = %q, want %q", act, ActionWrote)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != SkillContent() {
		t.Error("written file does not match the bundled skill")
	}
}

func TestInstallSkillIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if _, err := InstallSkill(path, false); err != nil {
		t.Fatal(err)
	}
	act, err := InstallSkill(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionUnchanged {
		t.Errorf("second install action = %q, want %q", act, ActionUnchanged)
	}
}

func TestInstallSkillOverwritesAManagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	stale := "---\nname: sonar\ndescription: old\n---\n" + managedMarker() + "\n\nold body\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	act, err := InstallSkill(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionWrote {
		t.Errorf("action = %q, want %q", act, ActionWrote)
	}
	got, _ := os.ReadFile(path)
	if string(got) != SkillContent() {
		t.Error("managed file should have been overwritten")
	}
}

func TestInstallSkillRefusesAnUnmanagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	mine := "---\nname: sonar\ndescription: hand written\n---\n\nmy own skill\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	act, err := InstallSkill(path, false)
	if err == nil {
		t.Fatal("expected an error refusing to overwrite an unmanaged file")
	}
	if act != ActionSkipped {
		t.Errorf("action = %q, want %q", act, ActionSkipped)
	}
	got, _ := os.ReadFile(path)
	if string(got) != mine {
		t.Error("unmanaged file must be left byte-identical")
	}
}

func TestInstallSkillForceOverwritesAnUnmanagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	act, err := InstallSkill(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionWrote {
		t.Errorf("action = %q, want %q", act, ActionWrote)
	}
	got, _ := os.ReadFile(path)
	if string(got) != SkillContent() {
		t.Error("--force should overwrite")
	}
}

func TestUninstallSkillRemovesOnlyManagedDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills", "sonar", "SKILL.md")
	if _, err := InstallSkill(path, false); err != nil {
		t.Fatal(err)
	}
	act, err := UninstallSkill(path)
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionRemoved {
		t.Errorf("action = %q, want %q", act, ActionRemoved)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Error("skill directory should be gone")
	}
}

func TestUninstallSkillLeavesAnUnmanagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	act, err := UninstallSkill(path)
	if err == nil {
		t.Fatal("expected an error refusing to remove an unmanaged file")
	}
	if act != ActionSkipped {
		t.Errorf("action = %q, want %q", act, ActionSkipped)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("unmanaged file must survive uninstall")
	}
}

func TestUninstallSkillOnMissingFile(t *testing.T) {
	act, err := UninstallSkill(filepath.Join(t.TempDir(), "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if act != ActionAbsent {
		t.Errorf("action = %q, want %q", act, ActionAbsent)
	}
}
