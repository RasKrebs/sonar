package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/groups"
)

// chdir moves into dir for the duration of a test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	was, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(was) })
}

// TestUpParamsUsesTheConfigInTheWorkingDirectory: with no argument, `sonar up`
// starts the project you are standing in.
func TestUpParamsUsesTheConfigInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: demo\nservices:\n  - name: api\n    cmd: uv run api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	upOnly = nil
	params, err := upParams(nil)
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if params.Name != nil {
		t.Errorf("name = %v, want nil when the group comes from the cwd", params.Name)
	}
	if params.ConfigPath == nil || filepath.Base(*params.ConfigPath) != groups.ConfigName {
		t.Fatalf("config_path = %v", params.ConfigPath)
	}
}

// TestUpParamsPassesTheGroupName straight through, so the daemon resolves it
// against every config it knows and not only the one under the cwd.
func TestUpParamsPassesTheGroupName(t *testing.T) {
	upOnly = []string{"api", "db"}
	t.Cleanup(func() { upOnly = nil })

	params, err := upParams([]string{"  storefront "})
	if err != nil {
		t.Fatalf("upParams: %v", err)
	}
	if params.Name == nil || *params.Name != "storefront" {
		t.Fatalf("name = %v, want storefront", params.Name)
	}
	if params.ConfigPath != nil {
		t.Errorf("config_path = %v, want nil when a group is named", params.ConfigPath)
	}
	if len(params.Only) != 2 || params.Only[0] != "api" {
		t.Errorf("only = %v", params.Only)
	}
}

// TestUpParamsWithoutAConfig says what to do next rather than just failing.
func TestUpParamsWithoutAConfig(t *testing.T) {
	chdir(t, t.TempDir())
	upOnly = nil

	_, err := upParams(nil)
	if err == nil {
		t.Fatal("expected an error with no .sonar.yaml anywhere")
	}
	if !strings.Contains(err.Error(), "sonar init") {
		t.Errorf("error should say how to make one: %v", err)
	}
}

// TestUpParamsReportsABrokenConfig instead of pretending there is none: the
// two problems have completely different fixes.
func TestUpParamsReportsABrokenConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName),
		[]byte("name: two words\nservices:\n  - name: api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	upOnly = nil

	_, err := upParams(nil)
	if err == nil {
		t.Fatal("expected an error for an invalid config")
	}
	if !strings.Contains(err.Error(), "cannot be used") || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("error = %v, want it to name the validation problem", err)
	}
}
