package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/doctor"
	"github.com/spf13/cobra"
)

func sampleReport() rpc.DaemonDoctorResult {
	return rpc.DaemonDoctorResult{
		OK:            false,
		Version:       "v1.2.3",
		DaemonVersion: "v1.2.3",
		Checks: []rpc.DoctorCheck{
			{ID: "cli_on_path", Status: doctor.StatusOK, Summary: "sonar resolves from PATH", Detail: "/usr/local/bin/sonar"},
			{ID: "cli_version_current", Status: doctor.StatusSkip, Summary: "development build"},
			{
				ID: "config_parses", Status: doctor.StatusFail,
				Summary: "config.yaml does not parse",
				Detail:  "/home/dev/.config/sonar/config.yaml:1:7: did not find expected ','\n  list: [broken\n        ^",
				Fix:     "sonar doctor --fix", Fixable: true,
			},
			{
				ID: "hooks_installed", Status: doctor.StatusWarn,
				Summary: "the sonar hooks are not installed",
				Fix:     "sonar install hooks --claude-code", Fixable: true,
			},
		},
	}
}

func TestRenderDoctorTable(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	var buf bytes.Buffer
	renderDoctor(&buf, sampleReport())
	out := buf.String()

	for _, want := range []string{
		"ok    cli_on_path",
		"skip  cli_version_current",
		"FAIL  config_parses",
		"warn  hooks_installed",
		// The fix hint rides on the rows that need acting on...
		"→ sonar install hooks --claude-code",
		// ...and the evidence is spelled out under a failure.
		"list: [broken",
		"^",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table is missing %q:\n%s", want, out)
		}
	}
	// A healthy row must not carry a fix hint or its detail.
	if strings.Contains(out, "/usr/local/bin/sonar") {
		t.Errorf("an ok row should not print its detail:\n%s", out)
	}
	if !strings.Contains(out, "something is wrong: 1 fail, 1 warn, 1 ok, 1 skip") {
		t.Errorf("verdict is missing or wrong:\n%s", out)
	}
}

func TestRenderDoctorVerdicts(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	cases := []struct {
		name   string
		report rpc.DaemonDoctorResult
		want   string
	}{
		{
			name: "all ok",
			report: rpc.DaemonDoctorResult{OK: true, Checks: []rpc.DoctorCheck{
				{ID: "a", Status: doctor.StatusOK, Summary: "fine"},
			}},
			want: "all good: 1 ok",
		},
		{
			name: "warnings only",
			report: rpc.DaemonDoctorResult{OK: true, Checks: []rpc.DoctorCheck{
				{ID: "a", Status: doctor.StatusWarn, Summary: "hmm"},
			}},
			want: "mostly healthy: 1 warn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderDoctor(&buf, tc.report)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("verdict = %q, want it to contain %q", buf.String(), tc.want)
			}
		})
	}
}

// TestDoctorJSON drives the command itself, so the JSON a script parses is the
// JSON this test asserts on.
func TestDoctorJSON(t *testing.T) {
	withDoctorFlags(t, func() {
		doctorJSONFlag = true
		doctorOnlyFlag = []string{"tray", "db_ok"}
	})

	out := captureStdout(t, func() {
		if err := doctorRun(testCommand(), nil); err != nil && !errors.Is(err, errSilent) {
			t.Fatalf("sonar doctor --json: %v", err)
		}
	})

	var got rpc.DaemonDoctorResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(got.Checks) != 2 {
		t.Fatalf("got %d checks, want the 2 that were selected: %+v", len(got.Checks), got.Checks)
	}
	ids := []string{got.Checks[0].ID, got.Checks[1].ID}
	if ids[0] != "db_ok" || ids[1] != "tray" {
		t.Errorf("ids = %v, want [db_ok tray] in table order", ids)
	}
	for _, c := range got.Checks {
		switch c.Status {
		case doctor.StatusOK, doctor.StatusWarn, doctor.StatusFail, doctor.StatusSkip:
		default:
			t.Errorf("%s has status %q", c.ID, c.Status)
		}
	}
	if got.Version == "" {
		t.Error("version is empty")
	}
}

// TestDoctorExitsNonZeroOnAFailure pins the contract a script depends on: the
// command returns errSilent, which Execute turns into exit 1 without printing
// anything more.
func TestDoctorExitsNonZeroOnAFailure(t *testing.T) {
	withDoctorFlags(t, func() {
		// No daemon runs in an isolated test binary, so this one fails.
		doctorOnlyFlag = []string{"daemon_reachable"}
	})
	err := errors.New("")
	captureStdout(t, func() { err = doctorRun(testCommand(), nil) })
	if !errors.Is(err, errSilent) {
		t.Fatalf("error = %v, want errSilent", err)
	}
}

func TestDoctorRejectsAnUnknownCheck(t *testing.T) {
	withDoctorFlags(t, func() { doctorOnlyFlag = []string{"not_a_check"} })
	err := doctorRun(testCommand(), nil)
	if err == nil || !strings.Contains(err.Error(), "not_a_check") {
		t.Fatalf("error = %v, want it to name the unknown check", err)
	}
}

func TestDoctorProjectMustBeADirectory(t *testing.T) {
	withDoctorFlags(t, func() { doctorProjectFlag = os.DevNull })
	if _, err := doctorProject(); err == nil {
		t.Fatal("a --project that is not a directory should be refused")
	}
}

// TestEveryFixableCheckHasAFix keeps the two lists in step: a check that
// advertises Fixable with no entry in doctorFixes would print an offer
// `--fix` cannot keep.
func TestEveryFixableCheckHasAFix(t *testing.T) {
	have := map[string]bool{}
	for _, f := range doctorFixes() {
		have[f.id] = true
	}
	// The ids the doctor package marks fixable, spelled out here so a new one
	// has to be considered rather than silently unhandled.
	for _, id := range []string{
		"config_parses", "daemon_reachable", "skills_installed", "hooks_installed",
		"mcp_registered.claude_code", "mcp_registered.cursor", "mcp_registered.codex",
	} {
		if !have[id] {
			t.Errorf("no fix registered for %s", id)
		}
	}
	for _, f := range doctorFixes() {
		if !contains(doctor.IDs(), f.id) {
			t.Errorf("fix %s repairs a check that does not exist", f.id)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- helpers ---

// withDoctorFlags sets the command's package-level flags for one test and puts
// them back afterwards.
func withDoctorFlags(t *testing.T, set func()) {
	t.Helper()
	oldJSON, oldFix, oldYes := doctorJSONFlag, doctorFixFlag, doctorYesFlag
	oldOnly, oldProject := doctorOnlyFlag, doctorProjectFlag
	t.Cleanup(func() {
		doctorJSONFlag, doctorFixFlag, doctorYesFlag = oldJSON, oldFix, oldYes
		doctorOnlyFlag, doctorProjectFlag = oldOnly, oldProject
	})
	doctorJSONFlag, doctorFixFlag, doctorYesFlag = false, false, false
	doctorOnlyFlag, doctorProjectFlag = nil, ""
	set()
}

func testCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}
