package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/config"
	"github.com/raskrebs/sonar/internal/testenv"
)

// The config this writes is the isolated one testenv gave this binary; the
// assertion that it really is isolated is testenv's own, and this test only
// exists to prove the two keys survive a round trip through YAML.
func TestConfigStoreRoundTrip(t *testing.T) {
	testenv.RequireIsolated(t, config.Path())

	store := ConfigStore{}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != (State{}) {
		t.Fatalf("a machine with no install loaded %+v", got)
	}

	want := State{Version: "0.1.0-beta.1", Path: filepath.Join(t.TempDir(), "Sonar.app")}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	if got, err = store.Load(); err != nil || got != want {
		t.Fatalf("loaded %+v (%v), want %+v", got, err, want)
	}

	// The keys are the ones the contract names, not whatever Go would have
	// picked from the field names.
	body, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"desktop:", "installed_version:", "installed_path:"} {
		if !strings.Contains(string(body), key) {
			t.Errorf("config.yaml has no %s:\n%s", key, body)
		}
	}

	// Saving does not disturb a neighbouring setting.
	if _, err := config.Apply(map[string]any{"list": map[string]any{"sort": "pid"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(State{Version: "0.2.0", Path: want.Path}); err != nil {
		t.Fatal(err)
	}
	cfg, warnings := config.Load()
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
	if cfg.List.Sort != "pid" {
		t.Errorf("list.sort = %q, want it untouched", cfg.List.Sort)
	}
	if cfg.Desktop.InstalledVersion != "0.2.0" {
		t.Errorf("desktop.installed_version = %q", cfg.Desktop.InstalledVersion)
	}
}

// The download_base key is read, not written: an installer must honour it and
// never rewrite what the user typed.
func TestDownloadBaseIsReadFromTheConfig(t *testing.T) {
	if _, err := config.Apply(map[string]any{"desktop": map[string]any{
		"download_base": "https://example.com/desktop",
	}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if got := ResolveBase("", "", cfg.Desktop.DownloadBase); got != "https://example.com/desktop" {
		t.Errorf("ResolveBase = %q", got)
	}
	if got := ResolveBase("", "https://env", cfg.Desktop.DownloadBase); got != "https://env" {
		t.Errorf("the environment should win over the config, got %q", got)
	}
}
