package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDesktopConfig puts the two keys `sonar install desktop` records into
// the fake env's config file.
func writeDesktopConfig(t *testing.T, env *Env, version, path string) {
	t.Helper()
	body := "desktop:\n"
	if version != "" {
		body += "  installed_version: " + version + "\n"
	}
	if path != "" {
		body += "  installed_path: " + path + "\n"
	}
	write(t, env.ConfigPath, body)
}

func TestDesktopInstalled(t *testing.T) {
	t.Run("recorded and present", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "darwin"
		app := filepath.Join(t.TempDir(), "Sonar.app")
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		writeDesktopConfig(t, env, "0.1.0-beta.1", app)

		got := run(t, env, checkDesktopInstalled)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Summary, "0.1.0-beta.1") {
			t.Errorf("summary = %q, want the version", got.Summary)
		}
		if got.Detail != app {
			t.Errorf("detail = %q, want %q", got.Detail, app)
		}
	})

	t.Run("nothing installed", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "darwin"
		got := run(t, env, checkDesktopInstalled)
		wantStatus(t, got, StatusWarn)
		if got.Fix != "sonar install desktop" {
			t.Errorf("fix = %q", got.Fix)
		}
	})

	t.Run("recorded but gone", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "darwin"
		gone := filepath.Join(t.TempDir(), "Sonar.app")
		writeDesktopConfig(t, env, "0.1.0", gone)

		got := run(t, env, checkDesktopInstalled)
		wantStatus(t, got, StatusWarn)
		if !strings.Contains(got.Detail, gone) {
			t.Errorf("detail = %q, want it to name the missing path", got.Detail)
		}
		if got.Fix != "sonar install desktop" {
			t.Errorf("fix = %q", got.Fix)
		}
	})

	t.Run("installed by something else on linux", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "linux"
		// The AppImage location `sonar install desktop` uses, with nothing in
		// the config: an app the user installed by hand still counts.
		appImage := filepath.Join(env.Home, ".local", "opt", "sonar-desktop", "Sonar.AppImage")
		write(t, appImage, "#!/bin/sh\n")

		got := run(t, env, checkDesktopInstalled)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, appImage) {
			t.Errorf("detail = %q, want it to name the AppImage", got.Detail)
		}
	})

	t.Run("skips on windows", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "windows"
		got := run(t, env, checkDesktopInstalled)
		wantStatus(t, got, StatusSkip)
		if !strings.Contains(got.Summary, "windows") {
			t.Errorf("summary = %q", got.Summary)
		}
	})
}

func TestDesktopInstalledIsInTheCheckList(t *testing.T) {
	var found bool
	for _, id := range IDs() {
		if id == "desktop_installed" {
			found = true
		}
	}
	if !found {
		t.Errorf("desktop_installed is not in %v", IDs())
	}
}
