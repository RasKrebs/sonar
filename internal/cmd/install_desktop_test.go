package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/desktop"
)

func TestInstallDesktopIsRegisteredWithItsFlags(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if c.Name() != "install" {
			continue
		}
		for _, s := range c.Commands() {
			if s.Name() != "desktop" {
				continue
			}
			found = true
			for _, name := range []string{"base", "version", "dir", "no-launch", "json", "force", "update", "check", "deb"} {
				if s.Flags().Lookup(name) == nil {
					t.Errorf("install desktop has no --%s", name)
				}
			}
		}
	}
	if !found {
		t.Fatal("`sonar install desktop` is not registered")
	}
}

func TestReportDesktopCheck(t *testing.T) {
	t.Run("behind exits non-zero", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() {
			err = reportDesktopResult(&desktop.Result{
				Action:           desktop.ActionChecked,
				Version:          "0.2.0",
				InstalledVersion: "0.1.0",
				Path:             "/Applications/Sonar.app",
			})
		})
		if !errors.Is(err, errSilent) {
			t.Errorf("err = %v, want the silent non-zero exit", err)
		}
		for _, want := range []string{"0.1.0", "/Applications/Sonar.app", "0.2.0", "--update"} {
			if !strings.Contains(out, want) {
				t.Errorf("output is missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("current exits zero", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() {
			err = reportDesktopResult(&desktop.Result{
				Action:           desktop.ActionChecked,
				Version:          "0.2.0",
				InstalledVersion: "0.2.0",
				Path:             "/Applications/Sonar.app",
				UpToDate:         true,
			})
		})
		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if !strings.Contains(out, "up to date") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("nothing installed", func(t *testing.T) {
		out := captureStdout(t, func() {
			_ = reportDesktopResult(&desktop.Result{Action: desktop.ActionChecked, Version: "0.2.0"})
		})
		if !strings.Contains(out, "not installed") {
			t.Errorf("output = %q, want it to say nothing is installed", out)
		}
	})
}

func TestReportDesktopUpToDateUpdate(t *testing.T) {
	out := captureStdout(t, func() {
		err := reportDesktopResult(&desktop.Result{
			Action:           desktop.ActionUpToDate,
			InstalledVersion: "0.2.0",
			Path:             "/Applications/Sonar.app",
		})
		if err != nil {
			t.Errorf("a no-op update is not a failure: %v", err)
		}
	})
	if !strings.Contains(out, "already installed") {
		t.Errorf("output = %q", out)
	}
}

func TestReportDesktopInstalled(t *testing.T) {
	out := captureStdout(t, func() {
		_ = reportDesktopResult(&desktop.Result{
			Action:   desktop.ActionInstalled,
			Version:  "0.1.0-beta.1",
			Path:     "/Applications/Sonar.app",
			Launched: true,
		})
	})
	for _, want := range []string{"0.1.0-beta.1", "/Applications/Sonar.app", "launched"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}
