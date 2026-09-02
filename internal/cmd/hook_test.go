package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookSessionStartWritesEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	in := strings.NewReader(`{"session_id":"abc123","cwd":"/home/u/projects/acme"}`)
	if err := runHookSessionStart(in, envFile); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "export SONAR_SESSION=claude-code:abc123\nexport SONAR_SESSION_LABEL=acme\n"
	if string(got) != want {
		t.Errorf("env file =\n%q\nwant\n%q", got, want)
	}
}

func TestHookSessionStartAppendsToExistingFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(envFile, []byte("export FOO=bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(`{"session_id":"abc","cwd":"/x/web"}`)
	if err := runHookSessionStart(in, envFile); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(envFile)
	want := "export FOO=bar\nexport SONAR_SESSION=claude-code:abc\nexport SONAR_SESSION_LABEL=web\n"
	if string(got) != want {
		t.Errorf("env file =\n%q\nwant\n%q", got, want)
	}
}

func TestHookSessionStartIsIdempotent(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	payload := `{"session_id":"abc","cwd":"/x/web"}`
	if err := runHookSessionStart(strings.NewReader(payload), envFile); err != nil {
		t.Fatal(err)
	}
	if err := runHookSessionStart(strings.NewReader(payload), envFile); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(envFile)
	if n := strings.Count(string(got), "SONAR_SESSION="); n != 1 {
		t.Errorf("SONAR_SESSION written %d times, want 1:\n%s", n, got)
	}
}

func TestHookSessionStartWithoutEnvFileIsANoop(t *testing.T) {
	if err := runHookSessionStart(strings.NewReader(`{"session_id":"abc"}`), ""); err != nil {
		t.Errorf("no CLAUDE_ENV_FILE must be a silent no-op, got %v", err)
	}
}

func TestHookSessionStartWithGarbageInputIsANoop(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	if err := runHookSessionStart(strings.NewReader("not json"), envFile); err != nil {
		t.Errorf("garbage stdin must never break the session, got %v", err)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Error("no env file should have been written")
	}
}

func TestHookSessionStartQuotesAwkwardLabels(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	in := strings.NewReader(`{"session_id":"abc","cwd":"/x/my project"}`)
	if err := runHookSessionStart(in, envFile); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(envFile)
	if !strings.Contains(string(got), "export SONAR_SESSION_LABEL='my project'\n") {
		t.Errorf("label was not quoted:\n%s", got)
	}
}

func TestHookPreBashAdvisesOnADevServer(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"npm run dev"}}`)
	if err := runHookPreBash(in, &out); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if payload.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q", payload.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "sonar start -- npm run dev") {
		t.Errorf("additionalContext missing the suggestion:\n%s", payload.HookSpecificOutput.AdditionalContext)
	}
}

func TestHookPreBashIsSilentOnUnrelatedCommands(t *testing.T) {
	for _, c := range []string{"ls -la", "npm run build", "sonar start -- npm run dev"} {
		var out bytes.Buffer
		in := strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c) + `}}`)
		if err := runHookPreBash(in, &out); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Errorf("Detect(%q) produced output: %s", c, out.String())
		}
	}
}

func TestHookPreBashIgnoresOtherTools(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(`{"tool_name":"Read","tool_input":{"command":"npm run dev"}}`)
	if err := runHookPreBash(in, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("non-Bash tool produced output: %s", out.String())
	}
}

func TestHookPreBashWithGarbageInputIsANoop(t *testing.T) {
	var out bytes.Buffer
	if err := runHookPreBash(strings.NewReader("not json"), &out); err != nil {
		t.Errorf("garbage stdin must never break the session, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("garbage stdin produced output: %s", out.String())
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
