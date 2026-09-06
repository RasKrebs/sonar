package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// actionHarness is the read harness plus the fake daemon's write half: a fake
// that can kill, rename, spawn and wait.
func actionHarness(t *testing.T) (*harness, *fakedaemon.Actions) {
	t.Helper()
	return actionHarnessWith(t, fakedaemon.DefaultFixture())
}

func actionHarnessWith(t *testing.T, fx fakedaemon.Fixture) (*harness, *fakedaemon.Actions) {
	t.Helper()
	h := newHarnessWith(t, fx)
	return h, h.fake.RegisterActions()
}

func TestToolsListAdvertisesTheActionTools(t *testing.T) {
	h := newHarness(t)

	res, err := h.client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}

	// The annotations spec 2 §1.1 fixes for each tool, which is what a client
	// gates a confirmation prompt on.
	want := map[string]struct{ readOnly, destructive, idempotent bool }{
		"kill":          {false, true, false},
		"stop_group":    {false, true, false},
		"rename_port":   {false, false, true},
		"start_service": {false, false, false},
		"list_groups":   {true, false, false},
	}
	for name, expect := range want {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("tools/list is missing %s", name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", name)
		}
		if tool.Annotations == nil {
			t.Fatalf("%s has no annotations", name)
		}
		if tool.Annotations.ReadOnlyHint != expect.readOnly {
			t.Errorf("%s readOnly = %v, want %v", name, tool.Annotations.ReadOnlyHint, expect.readOnly)
		}
		if !expect.readOnly {
			if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != expect.destructive {
				t.Errorf("%s destructive = %v, want %v", name, tool.Annotations.DestructiveHint, expect.destructive)
			}
		}
		if tool.Annotations.IdempotentHint != expect.idempotent {
			t.Errorf("%s idempotent = %v, want %v", name, tool.Annotations.IdempotentHint, expect.idempotent)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s should be closed-world: it only talks to the local daemon", name)
		}
	}

	if desc := byName["kill"].Description; !strings.Contains(desc, "dry_run") {
		t.Errorf("kill's description does not teach dry_run:\n%s", desc)
	}
	if desc := byName["start_service"].Description; !strings.Contains(desc, "wait_for_port") {
		t.Errorf("start_service's description does not teach wait_for_port:\n%s", desc)
	}
}

// TestKillPortStopsTheTree is the golden test for kill: the tree of the run
// behind port 3000 is two processes, and both come back as killed rows.
func TestKillPortStopsTheTree(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("kill", map[string]any{"port": 3000})
	out := structured[mcpserver.KillOutput](t, res)

	if len(out.Killed) != 2 || len(out.Failed) != 0 {
		t.Fatalf("killed %d, failed %d, want 2 and 0: %+v", len(out.Killed), len(out.Failed), out)
	}
	if out.DryRun {
		t.Error("a real kill reported itself as a dry run")
	}
	for _, row := range out.Killed {
		if row.Port != 3000 || row.Method != string(state.MethodSIGTERM) || row.Name == "" {
			t.Errorf("kill row is missing its port, name or method: %+v", row)
		}
	}
	if got, want := textOf(res), "Stopped 2 processes on port 3000 (sigterm)."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}

	// The kill is visible to the next read, the way the daemon's rescan makes
	// it (contract §22).
	ports := structured[mcpserver.ListPortsOutput](t, h.call("list_ports", map[string]any{}))
	for _, p := range ports.Ports {
		if p.Port == 3000 {
			t.Errorf("port 3000 is still listed after a kill")
		}
	}
}

func TestKillDryRunPlansAndActsOnNothing(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("kill", map[string]any{"port": 3000, "dry_run": true})
	out := structured[mcpserver.KillOutput](t, res)

	if len(out.Killed) != 2 || !out.DryRun {
		t.Fatalf("dry run = %+v, want two planned rows marked dry_run", out)
	}
	if got, want := textOf(res), "Dry run: would stop 2 processes on port 3000 (sigterm)."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}

	ports := structured[mcpserver.ListPortsOutput](t, h.call("list_ports", map[string]any{}))
	if !containsPort(ports.Ports, 3000) {
		t.Error("a dry run stopped something")
	}
}

// TestKillReportsAPermissionDeniedRow: a process this user may not signal is a
// failed row, not a failed call, so the rest of the kill still reports.
func TestKillReportsAPermissionDeniedRow(t *testing.T) {
	h, actions := actionHarness(t)
	actions.DenyKill(4101)
	actions.DenyKill(4100)

	res := h.call("kill", map[string]any{"port": 3000})
	if res.IsError {
		t.Fatalf("a failed row must not fail the call: %s", textOf(res))
	}
	out := structured[mcpserver.KillOutput](t, res)
	if len(out.Killed) != 0 || len(out.Failed) != 2 {
		t.Fatalf("killed %d, failed %d, want 0 and 2: %+v", len(out.Killed), len(out.Failed), out)
	}
	if !strings.Contains(out.Failed[0].Error, "not permitted to signal PID 4101") {
		t.Errorf("the daemon's reason was dropped: %+v", out.Failed[0])
	}
	if text := textOf(res); !strings.HasPrefix(text, "Stopped nothing on port 3000; 2 failed:") {
		t.Errorf("text = %q", text)
	}

	ports := structured[mcpserver.ListPortsOutput](t, h.call("list_ports", map[string]any{}))
	if !containsPort(ports.Ports, 3000) {
		t.Error("a port that could not be signalled disappeared from the list")
	}
}

// TestKillAPortNobodyIsOn: the daemon reports a failed row rather than an
// error, so one bad target in a batch does not lose the others.
func TestKillAPortNobodyIsOn(t *testing.T) {
	h, _ := actionHarness(t)

	out := structured[mcpserver.KillOutput](t, h.call("kill", map[string]any{"port": 9999}))
	if len(out.Killed) != 0 || len(out.Failed) != 1 {
		t.Fatalf("want one failed row: %+v", out)
	}
	if !strings.Contains(out.Failed[0].Error, "9999") {
		t.Errorf("the failed row does not name the port: %+v", out.Failed[0])
	}
}

func TestKillAnUnknownGroupIsNotFound(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("kill", map[string]any{"group": "nope"})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", payload.Error.Code)
	}
	if payload.Error.Hint == "" {
		t.Error("the daemon's hint was dropped")
	}
}

func TestKillBySessionNeedsTheCapability(t *testing.T) {
	h, _ := actionHarness(t) // the default fixture has no sessions capability

	res := h.call("kill", map[string]any{"session": "claude-code:9f2c"})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != mcpserver.CodeCapabilityMissing {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeCapabilityMissing)
	}
	if payload.Error.Hint == "" {
		t.Error("a capability_missing error must say what to do about it")
	}
	if n := h.fake.Calls("sessions.kill"); n != 0 {
		t.Errorf("the daemon was called %d times for a capability it does not have", n)
	}
}

func TestKillBySessionRoutesToSessionsKill(t *testing.T) {
	fx := fakedaemon.DefaultFixture()
	fx.Capabilities = append(fx.Capabilities, mcpserver.CapabilitySessions)
	h, _ := actionHarnessWith(t, fx)

	res := h.call("kill", map[string]any{"session": "claude-code:9f2c"})
	out := structured[mcpserver.KillOutput](t, res)

	if len(out.Killed) != 2 {
		t.Fatalf("killed %d rows, want the two processes of the session's run: %+v", len(out.Killed), out)
	}
	if n := h.fake.Calls("sessions.kill"); n != 1 {
		t.Errorf("sessions.kill was called %d times, want 1", n)
	}
	if text := textOf(res); !strings.Contains(text, "for session claude-code:9f2c") {
		t.Errorf("text = %q, want it to name the session", text)
	}
}

func TestKillDockerStopsTheContainer(t *testing.T) {
	h, _ := actionHarness(t)

	out := structured[mcpserver.KillOutput](t, h.call("kill", map[string]any{"port": 5432}))
	if len(out.Killed) != 1 || out.Killed[0].Method != string(state.MethodDockerStop) {
		t.Fatalf("want one docker_stop row: %+v", out)
	}
	if out.Killed[0].Name != "shop-db-1" {
		t.Errorf("the row does not name the container: %+v", out.Killed[0])
	}
}

func TestKillNeedsExactlyOneSelector(t *testing.T) {
	h, _ := actionHarness(t)

	for name, args := range map[string]map[string]any{
		"none": {},
		"two":  {"port": 3000, "group": "shop"},
	} {
		t.Run(name, func(t *testing.T) {
			res := h.call("kill", args)
			payload, ok := mcpserver.DecodeError(res)
			if !ok {
				t.Fatalf("expected an error result, got %s", textOf(res))
			}
			if payload.Error.Code != mcpserver.CodeInvalidArguments {
				t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
			}
			if n := h.fake.Calls("ports.kill"); n != 0 {
				t.Errorf("the daemon was called for an argument error")
			}
		})
	}
}

func TestKillWithoutTreeSignalsOneProcess(t *testing.T) {
	h, _ := actionHarness(t)

	out := structured[mcpserver.KillOutput](t, h.call("kill", map[string]any{"port": 3000, "tree": false}))
	if len(out.Killed) != 1 {
		t.Fatalf("tree: false killed %d rows, want 1: %+v", len(out.Killed), out)
	}
	if out.Killed[0].PID != 4101 {
		t.Errorf("killed pid %d, want the listener 4101", out.Killed[0].PID)
	}
}

func TestStopGroupStopsEveryMember(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("stop_group", map[string]any{"group": "shop"})
	out := structured[mcpserver.KillOutput](t, res)

	// api (a run: two processes) plus vite (one).
	if len(out.Killed) != 3 {
		t.Fatalf("killed %d rows, want 3: %+v", len(out.Killed), out)
	}
	if got, want := textOf(res), "Stopped 3 processes in group shop (sigterm)."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}

	ports := structured[mcpserver.ListPortsOutput](t, h.call("list_ports", map[string]any{"group": "shop"}))
	if len(ports.Ports) != 0 {
		t.Errorf("group shop still has %d ports listening", len(ports.Ports))
	}
}

func TestStopGroupDryRunAndUnknownGroup(t *testing.T) {
	h, _ := actionHarness(t)

	dry := structured[mcpserver.KillOutput](t, h.call("stop_group", map[string]any{"group": "shop", "dry_run": true}))
	if !dry.DryRun || len(dry.Killed) != 3 {
		t.Fatalf("dry run = %+v", dry)
	}

	res := h.call("stop_group", map[string]any{"group": "nope"})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", payload.Error.Code)
	}
}

func TestStopGroupNeedsAGroup(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("stop_group", map[string]any{"group": "   "})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
}

func TestRenamePortNamesTheRow(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("rename_port", map[string]any{"port": 5173, "name": "storefront"})
	out := structured[mcpserver.RenamePortOutput](t, res)

	if out.Port.Name == nil || *out.Port.Name != "storefront" {
		t.Fatalf("the row does not carry the new name: %+v", out.Port)
	}
	if out.Port.DisplayName != "storefront" {
		t.Errorf("display_name = %q, want storefront", out.Port.DisplayName)
	}
	if text := textOf(res); !strings.HasPrefix(text, `Port 5173 is now called "storefront".`) {
		t.Errorf("text = %q", text)
	}
	// The single-object rendering is still there under the sentence.
	if text := textOf(res); !strings.Contains(text, "project_root") {
		t.Errorf("the row was not rendered under the sentence:\n%s", text)
	}

	// Idempotent: the same call again reports the same row.
	again := structured[mcpserver.RenamePortOutput](t, h.call("rename_port", map[string]any{"port": 5173, "name": "storefront"}))
	if again.Port.DisplayName != "storefront" {
		t.Errorf("a repeated rename changed the row: %+v", again.Port)
	}
}

func TestRenamePortNeedsASelector(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("rename_port", map[string]any{"name": "api"})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
}

func TestRenamePortUnknownPort(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("rename_port", map[string]any{"port": 9999, "name": "api"})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", payload.Error.Code)
	}
}

func TestStartServiceWithoutWaitingReturnsTheRun(t *testing.T) {
	h, actions := actionHarness(t)
	clearAgentEnv(t)

	res := h.call("start_service", map[string]any{
		"command": []any{"python3", "-m", "http.server", "8000"},
		"cwd":     "/home/dev/shop",
		"name":    "docs",
	})
	out := structured[mcpserver.StartServiceOutput](t, res)

	if out.RunID == "" || out.PID == 0 || out.LogPath == "" {
		t.Fatalf("start_service dropped the run it started: %+v", out)
	}
	if out.Name != "docs" || out.Group != "shop" {
		t.Errorf("group/name = %q/%q, want shop/docs as the daemon resolved them", out.Group, out.Name)
	}
	if len(out.Ports) != 0 {
		t.Errorf("a start without wait_for_port cannot know a port yet: %+v", out.Ports)
	}
	if runs := actions.Runs(); len(runs) != 1 || runs[0].Cmd != "python3 -m http.server 8000" {
		t.Errorf("the daemon was asked for %+v", runs)
	}
	if text := textOf(res); !strings.Contains(text, "Started `python3 -m http.server 8000` as run ") {
		t.Errorf("text = %q", text)
	}
}

// TestStartServiceWaitsForAKnownPort drives the ports.wait stream: the service
// starts listening a moment after the spawn, and the tool returns its row.
func TestStartServiceWaitsForAKnownPort(t *testing.T) {
	h, actions := actionHarness(t)
	clearAgentEnv(t)
	openWhenSpawned(t, actions, 8000)

	res := h.call("start_service", map[string]any{
		"command":         []any{"python3", "-m", "http.server", "8000"},
		"cwd":             "/home/dev/shop",
		"name":            "docs",
		"wait_for_port":   8000,
		"timeout_seconds": 10,
	})
	out := structured[mcpserver.StartServiceOutput](t, res)

	if res.IsError || out.TimedOut {
		t.Fatalf("the wait failed: %s", textOf(res))
	}
	if len(out.Ports) != 1 || out.Ports[0].Port != 8000 {
		t.Fatalf("ports = %+v, want the row for 8000", out.Ports)
	}
	if out.Ports[0].Run == nil || out.Ports[0].Run.ID != out.RunID {
		t.Errorf("the port row is not attributed to the run: %+v", out.Ports[0])
	}
	if text := textOf(res); !strings.Contains(text, "listening on http://localhost:8000") {
		t.Errorf("text = %q, want it to report the URL", text)
	}
}

// TestStartServiceAutoTakesWhateverPortOpens is wait_for_port: "auto": the tool
// does not know the port, and finds it through the run's attribution.
func TestStartServiceAutoTakesWhateverPortOpens(t *testing.T) {
	h, actions := actionHarness(t)
	clearAgentEnv(t)
	openWhenSpawned(t, actions, 8123)

	res := h.call("start_service", map[string]any{
		"command":         []any{"npm", "run", "dev"},
		"cwd":             "/home/dev/shop",
		"wait_for_port":   "auto",
		"timeout_seconds": 10,
	})
	out := structured[mcpserver.StartServiceOutput](t, res)

	if res.IsError || out.TimedOut {
		t.Fatalf("the wait failed: %s", textOf(res))
	}
	if len(out.Ports) != 1 || out.Ports[0].Port != 8123 {
		t.Fatalf("ports = %+v, want the port the run opened", out.Ports)
	}
	// "auto" is polled from runs.list, not from ports.wait (contract §20).
	if n := h.fake.Calls("ports.wait"); n != 0 {
		t.Errorf("ports.wait was called %d times for an auto wait", n)
	}
}

// TestStartServiceTimesOutWithTheRunIntact: a timeout is an IsError result that
// still carries the run, because the next thing to do is read its log.
func TestStartServiceTimesOutWithTheRunIntact(t *testing.T) {
	h, _ := actionHarness(t)
	clearAgentEnv(t)

	res := h.call("start_service", map[string]any{
		"command":         []any{"python3", "-m", "http.server", "8100"},
		"cwd":             "/home/dev/shop",
		"wait_for_port":   8100,
		"timeout_seconds": 1,
	})
	if !res.IsError {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	payload, ok := mcpserver.DecodeError(res)
	if !ok || payload.Error.Code != mcpserver.CodeTimeout {
		t.Fatalf("error payload = %+v, want a timeout", payload)
	}

	out := decodeOutput[mcpserver.StartServiceOutput](t, res)
	if !out.TimedOut || out.RunID == "" || out.PID == 0 || out.LogPath == "" {
		t.Fatalf("a timed-out start lost the run: %+v", out)
	}
	if !strings.Contains(textOf(res), "nothing was listening on port 8100") {
		t.Errorf("text = %q", textOf(res))
	}
	if payload.Error.Hint == "" {
		t.Error("a timeout must say what to do next")
	}
}

// TestStartServiceWithoutAnAgentEnvironmentHasNoSession: outside a coding
// agent there is no session to attribute a run to, and the field is null
// rather than an invented object — the same shape Port.session has.
func TestStartServiceWithoutAnAgentEnvironmentHasNoSession(t *testing.T) {
	h, _ := actionHarness(t)
	clearAgentEnv(t)

	res := h.call("start_service", map[string]any{
		"command": []any{"npm", "run", "dev"},
		"cwd":     "/home/dev/shop",
	})
	if res.IsError {
		t.Fatalf("start_service failed: %s", textOf(res))
	}
	out := structured[mcpserver.StartServiceOutput](t, res)
	if out.Session != nil {
		t.Errorf("session = %+v, want null with no agent in the environment", out.Session)
	}
}

// TestStartServiceAttributesTheAgentSession is the other half: the session is
// detected in this process, because the daemon's own environment is a service
// manager's and never an agent's (spec 2 §3).
func TestStartServiceAttributesTheAgentSession(t *testing.T) {
	h, _ := actionHarness(t)
	clearAgentEnv(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "9f2c")
	t.Setenv("CLAUDECODE", "1")

	res := h.call("start_service", map[string]any{
		"command": []any{"npm", "run", "dev"},
		"cwd":     "/home/dev/shop",
	})
	if res.IsError {
		t.Fatalf("start_service failed: %s", textOf(res))
	}
	out := structured[mcpserver.StartServiceOutput](t, res)
	if out.Session == nil {
		t.Fatal("start_service dropped the agent session")
	}
	if out.Session.ID != "9f2c" || out.Session.Tool != "claude-code" {
		t.Errorf("session = %+v, want the claude-code session", out.Session)
	}
	if !out.Session.Detected {
		t.Error("a session read off CLAUDECODE is detected, not declared")
	}
}

// clearAgentEnv removes every marker sessions.Detect reads, so a test asserting
// on attribution says the same thing on a laptop inside a coding agent and on a
// bare CI runner.
func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SONAR_SESSION", "SONAR_SESSION_ID", "SONAR_SESSION_TOOL", "SONAR_SESSION_LABEL",
		"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID",
		"CODEX_SANDBOX", "CODEX_THREAD_ID",
		"CURSOR_AGENT", "CURSOR_AGENT_SESSION_ID",
	} {
		t.Setenv(key, "")
	}
}

func TestStartServiceRejectsABadWaitForPort(t *testing.T) {
	h, _ := actionHarness(t)

	for name, value := range map[string]any{
		"a word":   "later",
		"a bool":   true,
		"too high": 70000,
	} {
		t.Run(name, func(t *testing.T) {
			res := h.call("start_service", map[string]any{
				"command":       []any{"npm", "run", "dev"},
				"cwd":           "/home/dev/shop",
				"wait_for_port": value,
			})
			payload, ok := mcpserver.DecodeError(res)
			if !ok {
				t.Fatalf("expected an error result, got %s", textOf(res))
			}
			if payload.Error.Code != mcpserver.CodeInvalidArguments {
				t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
			}
			if n := h.fake.Calls("runs.spawn"); n != 0 {
				t.Errorf("a service was spawned despite the bad argument")
			}
		})
	}
}

func TestStartServiceNeedsACommand(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("start_service", map[string]any{"command": []any{}})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
}

// TestListGroupsMatchesGroupsList is the golden test: the structured content is
// what groups.list returned, field for field.
func TestListGroupsMatchesGroupsList(t *testing.T) {
	h, _ := actionHarness(t)

	res := h.call("list_groups", map[string]any{})
	out := structured[mcpserver.ListGroupsOutput](t, res)

	want := h.fake.Fixture().Groups
	if !jsonEqual(t, out.Groups, want) {
		t.Fatalf("list_groups returned\n%s\nwant\n%s", mustJSON(t, out.Groups), mustJSON(t, want))
	}

	text := textOf(res)
	if !strings.HasPrefix(text, "2 groups\n\nGROUP") {
		t.Errorf("the text block is not a table:\n%s", text)
	}
	for _, want := range []string{"shop", "shop-infra", "3000, 5173", "api, web"} {
		if !strings.Contains(text, want) {
			t.Errorf("the table omits %q:\n%s", want, text)
		}
	}
}

func TestListGroupsMarksStoppedServices(t *testing.T) {
	fx := fakedaemon.DefaultFixture()
	fx.Groups[0].Services[1].Running = false
	h, _ := actionHarnessWith(t, fx)

	text := textOf(h.call("list_groups", map[string]any{}))
	if !strings.Contains(text, "web (stopped)") {
		t.Errorf("a service nobody started is not marked:\n%s", text)
	}
}

func TestListGroupsWhenThereAreNone(t *testing.T) {
	fx := fakedaemon.DefaultFixture()
	fx.Groups = nil
	h, _ := actionHarnessWith(t, fx)

	res := h.call("list_groups", map[string]any{})
	out := structured[mcpserver.ListGroupsOutput](t, res)
	if out.Groups == nil || len(out.Groups) != 0 {
		t.Errorf("groups = %+v, want an empty array", out.Groups)
	}
	if !strings.HasPrefix(textOf(res), "no groups") {
		t.Errorf("text = %q", textOf(res))
	}
}

// openWhenSpawned plays the service coming up: as soon as runs.spawn has
// recorded a run, the port it was going to open starts listening.
func openWhenSpawned(t *testing.T, actions *fakedaemon.Actions, port int) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if runs := actions.Runs(); len(runs) > 0 {
				time.Sleep(50 * time.Millisecond)
				actions.OpenPortForRun(runs[0].ID, port)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { <-done })
}

// decodeOutput reads structured content off a result the tool marked as an
// error: start_service's timeout carries the run beside the error object.
func decodeOutput[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding structured content: %v\n%s", err, raw)
	}
	return out
}

func containsPort(ports []state.Port, want int) bool {
	for _, p := range ports {
		if p.Port == want {
			return true
		}
	}
	return false
}
