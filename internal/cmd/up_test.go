package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/spf13/cobra"
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

// upProfileHome gives the test its own HOME with one profile in it, so
// profile.List sees exactly what the test wrote.
func upProfileHome(t *testing.T, names ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(HintEnv, "")
	dir := filepath.Join(home, ".config", "sonar", "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte("ports: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resetHints()
}

// upNotFound is the error the daemon returns for `sonar up <name>` when no
// group of that name exists.
func upNotFound() error {
	return rpc.NewError(rpc.CodeNotFound, "no group named legacy", "run `sonar groups` to see what is there")
}

// upErrorCommand is a stand-in for the real `up` command: it has the --json
// flag Hint consults and a buffer for stderr.
func upErrorCommand(asJSON bool) (*cobra.Command, *bytes.Buffer) {
	var errOut bytes.Buffer
	cmd := &cobra.Command{Use: "up"}
	cmd.Flags().Bool("json", asJSON, "")
	_ = cmd.Flags().Set("json", strconv.FormatBool(asJSON))
	cmd.SetErr(&errOut)
	return cmd, &errOut
}

// TestUpNoticesAProfileThroughTheHintMechanism: `sonar up <profile>` used to
// glue an ad-hoc "note:" onto the error. It now goes through Hint like every
// other migration notice (§23), so there is one mechanism, one line, and the
// same SONAR_NO_HINTS and --json rules.
func TestUpNoticesAProfileThroughTheHintMechanism(t *testing.T) {
	upProfileHome(t, "legacy")

	cmd, errOut := upErrorCommand(false)
	err := upError(cmd, []string{"legacy"}, upNotFound())
	if err == nil {
		t.Fatal("upError swallowed the daemon's error")
	}
	if strings.Contains(err.Error(), "note:") {
		t.Errorf("the ad-hoc note is still glued onto the error: %v", err)
	}
	notice := errOut.String()
	if !strings.HasPrefix(notice, hintPrefix) || countNotices(notice) != 1 {
		t.Fatalf("stderr = %q, want exactly one hint line", notice)
	}
	if !strings.Contains(notice, "sonar profile export legacy") ||
		!strings.Contains(notice, groups.ConfigName) {
		t.Errorf("the notice does not say what to type next: %q", notice)
	}
}

func TestUpSaysNothingWhenTheNameIsNotAProfile(t *testing.T) {
	upProfileHome(t, "legacy")

	cmd, errOut := upErrorCommand(false)
	if err := upError(cmd, []string{"storefront"}, upNotFound()); err == nil {
		t.Fatal("upError swallowed the daemon's error")
	}
	if errOut.String() != "" {
		t.Fatalf("stderr = %q, want nothing for a name that is not a profile", errOut.String())
	}
}

func TestUpProfileNoticeObeysJSONAndNoHints(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		upProfileHome(t, "legacy")
		cmd, errOut := upErrorCommand(true)
		_ = upError(cmd, []string{"legacy"}, upNotFound())
		if errOut.String() != "" {
			t.Fatalf("stderr = %q, want nothing under --json", errOut.String())
		}
	})
	t.Run("SONAR_NO_HINTS", func(t *testing.T) {
		upProfileHome(t, "legacy")
		t.Setenv(HintEnv, "1")
		cmd, errOut := upErrorCommand(false)
		_ = upError(cmd, []string{"legacy"}, upNotFound())
		if errOut.String() != "" {
			t.Fatalf("stderr = %q, want nothing with %s set", errOut.String(), HintEnv)
		}
	})
}
