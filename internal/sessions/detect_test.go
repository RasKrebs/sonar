package sessions

import "testing"

// env builds a Getenv over a fixed table.
func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// procs is a fixed ancestry: 100 is the agent, 200 the shell it spawned, 300
// the sonar process doing the detecting.
func procs() []Process {
	return []Process{
		{PID: 300, PPID: 200, Command: "sonar"},
		{PID: 200, PPID: 100, Command: "/bin/zsh"},
		{PID: 100, PPID: 1, Command: "/opt/homebrew/bin/claude"},
	}
}

func TestDetectEnvMatrix(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantOK   bool
		id       string
		tool     string
		label    string
		detected bool
	}{
		{
			name:   "nothing",
			env:    map[string]string{},
			wantOK: false,
		},
		{
			name:   "SONAR_SESSION is the contract and wins",
			env:    map[string]string{EnvSession: "claude-code:abc123", EnvClaudeCode: "1"},
			wantOK: true, id: "abc123", tool: "claude-code", detected: false,
		},
		{
			name:   "SONAR_SESSION with a label",
			env:    map[string]string{EnvSession: "codex:t-9", EnvSessionLabel: "refactor the killer"},
			wantOK: true, id: "t-9", tool: "codex", label: "refactor the killer", detected: false,
		},
		{
			name:   "SONAR_SESSION without a tool prefix",
			env:    map[string]string{EnvSession: "just-an-id"},
			wantOK: true, id: "just-an-id", tool: ToolAgent, detected: false,
		},
		{
			name:   "generic SONAR_SESSION_ID",
			env:    map[string]string{EnvSessionID: "id-7", EnvSessionLabel: "nightly"},
			wantOK: true, id: "id-7", tool: ToolAgent, label: "nightly", detected: false,
		},
		{
			name:   "generic id with an explicit tool",
			env:    map[string]string{EnvSessionID: "id-7", EnvSessionTool: "aider"},
			wantOK: true, id: "id-7", tool: "aider", detected: false,
		},
		{
			name:   "Claude Code by CLAUDECODE, id from the ancestor pid",
			env:    map[string]string{EnvClaudeCode: "1"},
			wantOK: true, id: "pid-100", tool: ToolClaudeCode, detected: true,
		},
		{
			name:   "Claude Code with its own session id",
			env:    map[string]string{EnvClaudeCode: "1", EnvClaudeCodeSessionID: "cc-42"},
			wantOK: true, id: "cc-42", tool: ToolClaudeCode, detected: true,
		},
		{
			name:   "Claude Code from the session id alone",
			env:    map[string]string{EnvClaudeSessionID: "cc-legacy"},
			wantOK: true, id: "cc-legacy", tool: ToolClaudeCode, detected: true,
		},
		{
			name:   "CLAUDECODE=0 is not an agent",
			env:    map[string]string{EnvClaudeCode: "0"},
			wantOK: false,
		},
		{
			name:   "Codex by thread id",
			env:    map[string]string{EnvCodexThreadID: "thr-1"},
			wantOK: true, id: "thr-1", tool: ToolCodex, detected: true,
		},
		{
			name:   "Codex by sandbox marker falls back to the parent pid",
			env:    map[string]string{EnvCodexSandbox: "seatbelt"},
			wantOK: true, id: "pid-200", tool: ToolCodex, detected: true,
		},
		{
			name:   "Cursor",
			env:    map[string]string{EnvCursorAgent: "1", EnvCursorAgentSessionID: "cur-3"},
			wantOK: true, id: "cur-3", tool: ToolCursor, detected: true,
		},
		{
			name:   "Claude Code wins over Codex when both are set",
			env:    map[string]string{EnvClaudeCodeSessionID: "cc-1", EnvCodexThreadID: "thr-1"},
			wantOK: true, id: "cc-1", tool: ToolClaudeCode, detected: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Detect(Options{
				Getenv:    env(tc.env),
				PID:       300,
				Processes: procs,
			})
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (session %+v)", ok, tc.wantOK, got)
			}
			if !ok {
				return
			}
			if got.ID != tc.id {
				t.Errorf("id = %q, want %q", got.ID, tc.id)
			}
			if got.Tool != tc.tool {
				t.Errorf("tool = %q, want %q", got.Tool, tc.tool)
			}
			if got.Label != tc.label {
				t.Errorf("label = %q, want %q", got.Label, tc.label)
			}
			if got.Detected != tc.detected {
				t.Errorf("detected = %v, want %v", got.Detected, tc.detected)
			}
		})
	}
}

// A detected id is the agent's pid, not this process's: two commands the same
// agent starts must land in one session.
func TestDetectedIDIsStableAcrossCommands(t *testing.T) {
	opts := func(pid int) Options {
		return Options{Getenv: env(map[string]string{EnvClaudeCode: "1"}), PID: pid, Processes: procs}
	}
	first, _ := Detect(opts(300))
	second, _ := Detect(opts(200))
	if first.ID != second.ID {
		t.Errorf("two commands of one agent got %q and %q", first.ID, second.ID)
	}
	if first.ID != "pid-100" {
		t.Errorf("id = %q, want the agent's pid", first.ID)
	}
}

func TestDetectFromEnvReadsAKeyValueSlice(t *testing.T) {
	got, ok := DetectFromEnv([]string{"PATH=/bin", "SONAR_SESSION=cursor:c-1"}, Options{})
	if !ok || got.ID != "c-1" || got.Tool != ToolCursor {
		t.Fatalf("DetectFromEnv = %+v, %v", got, ok)
	}
}
