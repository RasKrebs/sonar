// Package sessions attributes what sonar starts to the agent session that
// asked for it: which coding agent, which checkout, which branch.
//
// The identity contract is spec 2 §3. `SONAR_SESSION` is the explicit form and
// always wins — sonar never guesses when it is set. Everything else is
// best-effort detection from the environment an agent leaves behind, marked
// Detected so a client can say "probably Claude Code" rather than claiming it.
//
// Detection happens once, in the process that spawns the command (`sonar
// start`, or the daemon serving `runs.spawn`); the session then travels with
// the run and is stamped onto every port that run opens.
package sessions

import (
	"os"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/state"
)

// The environment variables that name a session (spec 2 §3, plus the two
// SONAR_SESSION_ID / SONAR_SESSION_TOOL spellings recorded under Contract
// notes for callers that have an id but no tool string to prefix it with).
const (
	EnvSession      = "SONAR_SESSION"       // "<tool>:<id>", the explicit contract
	EnvSessionID    = "SONAR_SESSION_ID"    // an id on its own
	EnvSessionTool  = "SONAR_SESSION_TOOL"  // the tool for EnvSessionID
	EnvSessionLabel = "SONAR_SESSION_LABEL" // human label, any form

	EnvClaudeCode           = "CLAUDECODE"
	EnvClaudeCodeSessionID  = "CLAUDE_CODE_SESSION_ID"
	EnvClaudeSessionID      = "CLAUDE_SESSION_ID"
	EnvCodexSandbox         = "CODEX_SANDBOX"
	EnvCodexThreadID        = "CODEX_THREAD_ID"
	EnvCursorAgent          = "CURSOR_AGENT"
	EnvCursorAgentSessionID = "CURSOR_AGENT_SESSION_ID"
)

// Tool names published in Session.tool. They are the stable strings clients
// switch on to pick an icon, so they never change spelling.
const (
	ToolClaudeCode = "claude-code"
	ToolCodex      = "codex"
	ToolCursor     = "cursor"
	// ToolAgent is what SONAR_SESSION_ID alone means: an agent that named
	// itself an id but not a tool.
	ToolAgent = "agent"
)

// Process is one row of the ancestry walk: enough to find the nearest ancestor
// that is the agent itself, so a detected session keeps one id for the life of
// that agent process rather than changing with every command it runs.
type Process struct {
	PID     int
	PPID    int
	Command string
}

// Options is what Detect reads. The zero value reads the real environment, the
// real pid and the real process table; tests fill in the fields they need.
type Options struct {
	// Getenv looks a variable up. Nil means os.Getenv.
	Getenv func(string) string
	// PID is the process detection starts walking up from. Zero means os.Getpid.
	PID int
	// Processes lists the process table for the ancestor walk. Nil means the
	// platform's own. A nil return falls back to the parent of PID.
	Processes func() []Process
}

// maxAncestry bounds the ancestor walk against a cyclic process table.
const maxAncestry = 64

// Detect resolves the agent session that spawned this process.
//
// ok is false when nothing in the environment names an agent: a plain shell
// starts a plain run, and inventing a session for it would attribute every dev
// server on the machine to something. Detected on the returned session
// distinguishes "the agent told us" (false) from "we recognised its
// environment" (true).
func Detect(opts Options) (state.Session, bool) {
	get := opts.Getenv
	if get == nil {
		get = os.Getenv
	}
	label := strings.TrimSpace(get(EnvSessionLabel))

	// 1. The explicit contract. `<tool>:<id>`; a value with no colon is an id
	//    from a tool that did not say which it is.
	if raw := strings.TrimSpace(get(EnvSession)); raw != "" {
		tool, id, found := strings.Cut(raw, ":")
		if !found || strings.TrimSpace(id) == "" {
			tool, id = ToolAgent, raw
		}
		return session(strings.TrimSpace(tool), strings.TrimSpace(id), label, false), true
	}

	// 2. An id on its own, for a caller that has one but no tool prefix.
	if id := strings.TrimSpace(get(EnvSessionID)); id != "" {
		tool := strings.TrimSpace(get(EnvSessionTool))
		if tool == "" {
			tool = ToolAgent
		}
		return session(tool, id, label, false), true
	}

	// 3. Best-effort detection, in the order spec 2 §3 lists the markers.
	for _, m := range markers {
		id, hit := m.match(get)
		if !hit {
			continue
		}
		if id == "" {
			id = ancestorID(opts, m.tool)
		}
		return session(m.tool, id, label, true), true
	}
	return state.Session{}, false
}

// DetectFromEnv is Detect over a `KEY=VALUE` slice, which is the shape an
// environment arrives in over the wire (`runs.spawn {env}`) and the shape
// exec.Cmd carries.
func DetectFromEnv(env []string, opts Options) (state.Session, bool) {
	table := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			table[k] = v
		}
	}
	opts.Getenv = func(k string) string { return table[k] }
	return Detect(opts)
}

// marker is one row of the detection matrix: the variables that say a tool is
// present, and the variable that carries its own session id when it has one.
type marker struct {
	tool     string
	present  []string
	idFrom   []string
	commands []string
}

// markers is spec 2 §3's table. Claude Code first: CLAUDECODE=1 is the marker
// the agent itself exports, and CLAUDE_CODE_SESSION_ID is what the SessionStart
// hook and the skill set (Contract notes).
var markers = []marker{
	{
		tool:     ToolClaudeCode,
		present:  []string{EnvClaudeCode, EnvClaudeCodeSessionID, EnvClaudeSessionID},
		idFrom:   []string{EnvClaudeCodeSessionID, EnvClaudeSessionID},
		commands: []string{"claude"},
	},
	{
		tool:     ToolCodex,
		present:  []string{EnvCodexThreadID, EnvCodexSandbox},
		idFrom:   []string{EnvCodexThreadID},
		commands: []string{"codex"},
	},
	{
		tool:     ToolCursor,
		present:  []string{EnvCursorAgent, EnvCursorAgentSessionID},
		idFrom:   []string{EnvCursorAgentSessionID},
		commands: []string{"cursor-agent", "cursor"},
	},
}

// match reports whether this tool's environment is present and, when it is,
// the id the tool named itself ("" when it named none).
func (m marker) match(get func(string) string) (string, bool) {
	hit := false
	for _, key := range m.present {
		if isSet(get(key)) {
			hit = true
			break
		}
	}
	if !hit {
		return "", false
	}
	for _, key := range m.idFrom {
		if v := strings.TrimSpace(get(key)); v != "" {
			return v, true
		}
	}
	return "", true
}

// isSet treats the shell's own idea of "off" as unset: CLAUDECODE=0 in a
// hand-written script means the agent is not there.
func isSet(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// ancestorID is the fallback id for a tool that names none: the pid of the
// nearest ancestor whose command is the agent, which is stable for the life of
// that agent process — exactly the lifetime cleanup cares about.
func ancestorID(opts Options, tool string) string {
	pid := opts.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	list := opts.Processes
	if list == nil {
		list = processTable
	}

	if procs := list(); len(procs) > 0 {
		byPID := make(map[int]Process, len(procs))
		for _, p := range procs {
			byPID[p.PID] = p
		}
		names := commandsFor(tool)
		cur := pid
		for i := 0; i < maxAncestry; i++ {
			p, ok := byPID[cur]
			if !ok {
				break
			}
			if matchesCommand(p.Command, names) {
				return pidID(p.PID)
			}
			if p.PPID <= 1 || p.PPID == cur {
				break
			}
			cur = p.PPID
		}
		// The agent's environment is here but the agent is not in our
		// ancestry — a hook exported the variable into a shell the agent then
		// detached from. The immediate parent still outlives this command,
		// which exits in seconds, so it is the better id of the two.
		if self, ok := byPID[pid]; ok && self.PPID > 1 {
			return pidID(self.PPID)
		}
	}
	if ppid := os.Getppid(); ppid > 1 {
		return pidID(ppid)
	}
	return pidID(pid)
}

func commandsFor(tool string) []string {
	for _, m := range markers {
		if m.tool == tool {
			return m.commands
		}
	}
	return []string{tool}
}

// matchesCommand compares a process command against the agent's binary names,
// tolerating a full path and a Windows .exe suffix.
func matchesCommand(cmd string, names []string) bool {
	base := strings.ToLower(strings.TrimSpace(cmd))
	if base == "" {
		return false
	}
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".exe")
	for _, n := range names {
		if base == n {
			return true
		}
	}
	return false
}

func pidID(pid int) string { return "pid-" + strconv.Itoa(pid) }

// session builds the wire object, leaving the git context to Capture.
func session(tool, id, label string, detected bool) state.Session {
	if id == "" {
		id = pidID(os.Getpid())
	}
	return state.Session{
		ID:       id,
		Tool:     tool,
		Label:    label,
		Detected: detected,
	}
}
