package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/raskrebs/sonar/internal/hooks"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Entry points invoked by Claude Code hooks",
	Long: "Entry points invoked by Claude Code hooks.\n\n" +
		"These read a hook payload on stdin and are not meant to be run by hand.\n" +
		"Install them with `sonar install hooks --claude-code`.",
}

var hookSessionStartCmd = &cobra.Command{
	Use:    "session-start",
	Short:  "Export SONAR_SESSION for the rest of the Claude Code session",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSessionStart(os.Stdin, os.Getenv("CLAUDE_ENV_FILE"))
	},
}

var hookPreBashCmd = &cobra.Command{
	Use:    "pre-bash",
	Short:  "Suggest `sonar start --` when a bare dev server is about to run",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookPreBash(os.Stdin, os.Stdout)
	},
}

func init() {
	// Hooks run on every Bash tool call and their stdout/stderr end up in the
	// agent's transcript, so they must stay silent. Overriding the root
	// PersistentPreRun (cobra runs only the closest one in the chain) skips
	// config loading and its warnings; hooks need neither.
	hookCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {}
	hookCmd.AddCommand(hookSessionStartCmd, hookPreBashCmd)
	rootCmd.AddCommand(hookCmd)
}

// sessionStartPayload is the subset of the SessionStart hook JSON sonar uses.
type sessionStartPayload struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
}

// runHookSessionStart appends SONAR_SESSION and SONAR_SESSION_LABEL to the
// file named by CLAUDE_ENV_FILE, so every Bash call in the session inherits
// them. A hook must never break the session, so every failure is a silent
// no-op rather than an error.
func runHookSessionStart(stdin io.Reader, envFile string) error {
	var p sessionStartPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		return nil
	}
	if envFile == "" || p.SessionID == "" {
		return nil
	}

	cwd := p.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	line := "export SONAR_SESSION=" + shellQuote("claude-code:"+p.SessionID)
	if existing, err := os.ReadFile(envFile); err == nil {
		if strings.Contains(string(existing), line) {
			return nil // SessionStart also fires on resume and compact
		}
	}

	body := line + "\n"
	if label := filepath.Base(cwd); label != "" && label != "." && label != string(os.PathSeparator) {
		body += "export SONAR_SESSION_LABEL=" + shellQuote(label) + "\n"
	}

	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	defer f.Close()
	_, _ = f.WriteString(body)
	return nil
}

// preBashPayload is the subset of the PreToolUse hook JSON sonar uses.
type preBashPayload struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// runHookPreBash prints additionalContext when the command about to run is a
// bare dev server. Advise mode always allows the call; strict mode (blocking
// with exit code 2) is deferred to a later slice.
func runHookPreBash(stdin io.Reader, stdout io.Writer) error {
	var p preBashPayload
	if err := json.NewDecoder(stdin).Decode(&p); err != nil {
		return nil
	}
	if p.ToolName != "" && p.ToolName != "Bash" {
		return nil
	}
	m, ok := hooks.Detect(p.ToolInput.Command)
	if !ok {
		return nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PreToolUse",
			"additionalContext": advice(m),
		},
	}
	enc := json.NewEncoder(stdout)
	// The advice is read by a human in the transcript; \u003c helps nobody.
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
	return nil
}

func advice(m hooks.Match) string {
	return fmt.Sprintf(
		"sonar: `%s` starts a long-lived server. Prefer `%s` so the process is "+
			"attributed to this session, its logs are captured, and it can be "+
			"cleaned up later with `sonar kill --session $SONAR_SESSION`. Then "+
			"wait for it with `sonar wait <port>` instead of sleeping. Do not "+
			"background it with `&` or `nohup`.",
		m.Segment, m.Suggest)
}

// shellQuote makes a value safe to paste after `export NAME=`.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`&|;<>()*?[]{}#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
