package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/sessions"
	"github.com/raskrebs/sonar/internal/state"
)

// `--session current` is spec 2 §3's shorthand: it resolves from this shell's
// own environment, and says so when there is nothing to resolve.
func TestCurrentSessionResolvesFromTheEnvironment(t *testing.T) {
	t.Setenv(sessions.EnvSession, "claude-code:abc123")
	got, err := currentSession("current")
	if err != nil {
		t.Fatalf("currentSession: %v", err)
	}
	if got != "abc123" {
		t.Errorf("currentSession(current) = %q, want abc123", got)
	}

	// Anything else is passed through untouched.
	if got, err := currentSession("some-id"); err != nil || got != "some-id" {
		t.Errorf("currentSession(some-id) = %q, %v", got, err)
	}
	if got, err := currentSession(""); err != nil || got != "" {
		t.Errorf("currentSession(\"\") = %q, %v", got, err)
	}
}

func TestCurrentSessionWithoutAnAgentIsAnError(t *testing.T) {
	for _, key := range []string{
		sessions.EnvSession, sessions.EnvSessionID, sessions.EnvClaudeCode,
		sessions.EnvClaudeCodeSessionID, sessions.EnvClaudeSessionID,
		sessions.EnvCodexThreadID, sessions.EnvCodexSandbox, sessions.EnvCursorAgent,
		sessions.EnvCursorAgentSessionID,
	} {
		t.Setenv(key, "")
	}
	if _, err := currentSession("current"); err == nil {
		t.Error("currentSession(current) outside an agent did not fail")
	}
}

// --session is daemon state, so the direct-scan path refuses rather than
// printing an empty table (contract §20 is about reads that *can* fall back).
func TestListSessionNeedsADaemon(t *testing.T) {
	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) { return nil, client.ErrNotRunning }
	t.Cleanup(func() { dialDaemon = prev })

	_, _, err := listPorts(context.Background(), listQuery{session: "abc"})
	if !errors.Is(err, errSessionNeedsDaemon) {
		t.Errorf("listPorts with --session and no daemon = %v, want errSessionNeedsDaemon", err)
	}
}

func TestSessionsCommandWithoutADaemonSaysSo(t *testing.T) {
	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) { return nil, client.ErrNotRunning }
	t.Cleanup(func() { dialDaemon = prev })

	_, err := sessionsDaemon(context.Background())
	if err == nil {
		t.Fatal("sessionsDaemon without a daemon did not fail")
	}
	if !strings.Contains(err.Error(), "sonar serve") {
		t.Errorf("error = %q, want it to name the command that starts a daemon", err)
	}
}

// A detected session is rendered with a marker, so a reader can tell "the
// agent told us" from "we recognised its environment" (spec 2 §3).
func TestToolLabelMarksADetectedSession(t *testing.T) {
	told := toolLabel(state.Session{Tool: sessions.ToolClaudeCode})
	guessed := toolLabel(state.Session{Tool: sessions.ToolClaudeCode, Detected: true})
	if told == guessed {
		t.Errorf("a detected session renders identically to a declared one: %q", told)
	}
	if !strings.Contains(guessed, sessions.ToolClaudeCode) {
		t.Errorf("label = %q, want it to name the tool", guessed)
	}
	if got := toolLabel(state.Session{}); got == "" {
		t.Error("a session with no tool rendered as an empty label")
	}
}

func TestSessionStateReadsActive(t *testing.T) {
	if !strings.Contains(sessionState(state.SessionRecord{Active: true}), "active") {
		t.Error("an active session is not labelled active")
	}
	if !strings.Contains(sessionState(state.SessionRecord{}), "inactive") {
		t.Error("an inactive session is not labelled inactive")
	}
}
