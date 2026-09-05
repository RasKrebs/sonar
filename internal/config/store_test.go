package config

import (
	"os"
	"path/filepath"
	"testing"
)

// tempHome points Path() at a scratch directory for the duration of a test.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".config", "sonar")
}

func TestMapOnAMissingFileIsEmpty(t *testing.T) {
	tempHome(t)
	m, err := Map()
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("Map = %v, want empty", m)
	}
}

func TestApplyWritesTheFileAndKeepsABackup(t *testing.T) {
	dir := tempHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("list:\n  sort: port\n  filter: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Apply(map[string]any{"list": map[string]any{"sort": "pid"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	list, _ := got["list"].(map[string]any)
	if list["sort"] != "pid" {
		t.Errorf("list.sort = %v, want pid", list["sort"])
	}
	if list["filter"] != "docker" {
		t.Errorf("list.filter = %v, want docker: a patch merges, it does not replace", list["filter"])
	}

	// The file on disk agrees with what was returned.
	reread, err := Map()
	if err != nil {
		t.Fatal(err)
	}
	if reread["list"].(map[string]any)["sort"] != "pid" {
		t.Errorf("reread config = %v, want list.sort pid", reread)
	}

	bak, err := os.ReadFile(BackupPath())
	if err != nil {
		t.Fatalf("reading the backup: %v", err)
	}
	if string(bak) != "list:\n  sort: port\n  filter: docker\n" {
		t.Errorf("backup = %q, want the file as it was before the write", bak)
	}
}

func TestApplyClearsAKeyWithNull(t *testing.T) {
	tempHome(t)
	if _, err := Apply(map[string]any{"color": false}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := Apply(map[string]any{"color": nil})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := got["color"]; ok {
		t.Errorf("color survived a null patch: %v", got)
	}
}

func TestApplyRejectsAnInvalidValueAndLeavesTheFileAlone(t *testing.T) {
	tempHome(t)
	if _, err := Apply(map[string]any{"list": map[string]any{"sort": "port"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(map[string]any{"list": map[string]any{"sort": "sideways"}}); err == nil {
		t.Fatal("Apply accepted an invalid sort")
	}
	cfg, _ := Load()
	if cfg.List.Sort != "port" {
		t.Errorf("list.sort = %q, want the rejected write to have changed nothing", cfg.List.Sort)
	}
}

func TestNumericKeysSurviveARoundTrip(t *testing.T) {
	tempHome(t)
	got, err := Apply(map[string]any{"services": map[string]any{"9000": "php-fpm"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got["services"].(map[string]any)["9000"] != "php-fpm" {
		t.Errorf("services = %v, want the port keyed as a string on the wire", got["services"])
	}
	cfg, warnings := Load()
	if len(warnings) > 0 {
		t.Errorf("re-reading the written config warned: %v", warnings)
	}
	if cfg.Services[9000] != "php-fpm" {
		t.Errorf("services = %v, want 9000 -> php-fpm parsed back as an int key", cfg.Services)
	}
}

func TestUnknownKeysSurviveAWrite(t *testing.T) {
	dir := tempHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("remote:\n  hosts: [me@box]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Apply(map[string]any{"color": true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	remote, ok := got["remote"].(map[string]any)
	if !ok || len(remote["hosts"].([]any)) != 1 {
		t.Errorf("config = %v, want the unknown remote.hosts key preserved", got)
	}
}
