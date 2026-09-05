package groups

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReloadPicksUpANewFile is the case the daemon's long-lived index cannot
// handle on its own (contract §18): a project configured after the daemon
// started becomes a group on the next reload.
func TestReloadPicksUpANewFile(t *testing.T) {
	dir := t.TempDir()
	x := NewIndex()

	if loaded, bad := x.Reload([]string{dir}); loaded != 0 || len(bad) != 0 {
		t.Fatalf("empty dir loaded %d configs, %v", loaded, bad)
	}

	writeConfig(t, dir, "name: later\nservices:\n  - name: api\n    port: 8000\n")

	loaded, bad := x.Reload([]string{dir})
	if loaded != 1 || len(bad) != 0 {
		t.Fatalf("reload loaded %d configs, errors %v", loaded, bad)
	}
	cfg, ok := x.Named("later")
	if !ok || cfg.Services[0].Name != "api" {
		t.Fatalf("the new config is not in the index: %+v", cfg)
	}
}

// TestReloadPicksUpAnEdit and forgets a file that was deleted.
func TestReloadPicksUpAnEditAndADelete(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n")
	x := NewIndex()
	x.Observe(dir)

	if _, ok := x.Named("demo"); !ok {
		t.Fatal("Observe did not index the config")
	}
	if x.Stale() {
		t.Fatal("a freshly read index reports itself stale")
	}

	writeConfig(t, dir, "name: renamed\nservices:\n  - name: api\n  - name: web\n")
	if !x.Stale() {
		t.Fatal("an edited file did not register as stale")
	}
	if loaded, bad := x.Reload(nil); loaded != 1 || len(bad) != 0 {
		t.Fatalf("reload after an edit: %d, %v", loaded, bad)
	}
	cfg, ok := x.Named("renamed")
	if !ok || len(cfg.Services) != 2 {
		t.Fatalf("the edit was not picked up: %+v", cfg)
	}
	if _, gone := x.Named("demo"); gone {
		t.Error("the old group name survived the reload")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !x.Stale() {
		t.Fatal("a deleted file did not register as stale")
	}
	if loaded, _ := x.Reload(nil); loaded != 0 {
		t.Fatalf("a deleted config is still loaded: %d", loaded)
	}
	if len(x.Configs()) != 0 {
		t.Errorf("Configs still returns %d entries", len(x.Configs()))
	}
}

// TestReloadReportsInvalidFiles: a broken file is an error the caller reports,
// never a fatal one, and fixing it is picked up by the next reload.
func TestReloadReportsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "name: bad name\nservices:\n  - name: api\n")

	x := NewIndex()
	loaded, bad := x.Reload([]string{dir})
	if loaded != 0 || len(bad) != 1 {
		t.Fatalf("reload = %d, %v", loaded, bad)
	}
	if bad[0].Path != filepath.Join(dir, ConfigName) {
		t.Errorf("error names %s", bad[0].Path)
	}

	// Two stats can land in the same filesystem timestamp; the size changes
	// too, which is why Stale looks at both.
	time.Sleep(10 * time.Millisecond)
	writeConfig(t, dir, "name: fixed\nservices:\n  - name: api\n")
	if !x.Stale() {
		t.Fatal("fixing an invalid file did not register as stale")
	}
	if loaded, bad := x.Reload(nil); loaded != 1 || len(bad) != 0 {
		t.Fatalf("reload after the fix: %d, %v", loaded, bad)
	}
}

// TestKnownAndByPath are what the daemon's config handlers resolve through.
func TestKnownAndByPath(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "name: demo\nservices:\n  - name: api\n")
	x := NewIndex()
	x.Observe(dir)

	// Observe resolves symlinks, so on macOS the indexed path is the
	// /private/var form of the temp dir; that is the path the daemon serves.
	known := x.Known()
	if len(known) != 1 {
		t.Fatalf("Known() = %v", known)
	}
	if filepath.Base(known[0]) != ConfigName {
		t.Fatalf("Known() = %v", known)
	}
	if cfg, ok := x.ByPath(known[0]); !ok || cfg.Name != "demo" {
		t.Fatalf("ByPath(%s) = %+v, %v", known[0], cfg, ok)
	}
	// The path the user typed resolves too, symlinked temp dir and all.
	if cfg, ok := x.ByPath(path); !ok || cfg.Name != "demo" {
		t.Fatalf("ByPath(%s) = %+v, %v", path, cfg, ok)
	}
	if _, ok := x.ByPath(filepath.Join(dir, "nope.yaml")); ok {
		t.Error("ByPath matched a file that is not indexed")
	}
}
