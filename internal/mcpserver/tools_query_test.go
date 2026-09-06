package mcpserver_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// queryFixture is the default machine plus the capabilities the query tools
// gate on. The default fixture deliberately lacks them, so the
// capability_missing paths have a fixture of their own.
func queryFixture(extra ...string) fakedaemon.Fixture {
	fx := fakedaemon.DefaultFixture()
	fx.Capabilities = append(append([]string{}, fx.Capabilities...), extra...)
	return fx
}

// fixtureWithout is the default machine with a capability taken away, for the
// tools that have to answer for a daemon that cannot help them.
func fixtureWithout(capability string) fakedaemon.Fixture {
	fx := fakedaemon.DefaultFixture()
	kept := make([]string, 0, len(fx.Capabilities))
	for _, c := range fx.Capabilities {
		if c != capability {
			kept = append(kept, c)
		}
	}
	fx.Capabilities = kept
	return fx
}

func TestQueryToolsAreAdvertised(t *testing.T) {
	h := newHarness(t)

	res, err := h.client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}

	readOnly := []string{
		"wait_for_port", "next_free_port", "tail_logs", "health_check",
		"dependency_graph", "port_history", "list_sessions",
	}
	for _, name := range append(readOnly, "claim_port") {
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
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s should be closed-world: it only talks to the local daemon", name)
		}
	}
	for _, name := range readOnly {
		if !byName[name].Annotations.ReadOnlyHint {
			t.Errorf("%s should be annotated read-only", name)
		}
	}
	// claim_port writes a reservation, so it is not read-only; claiming twice
	// returns the same ports, so it is idempotent (spec 2 §1.1).
	if byName["claim_port"].Annotations.ReadOnlyHint {
		t.Error("claim_port is annotated read-only, but it writes a claim")
	}
	if !byName["claim_port"].Annotations.IdempotentHint {
		t.Error("claim_port should be annotated idempotent")
	}
}

// ------------------------------------------------------------ wait_for_port ---

func TestWaitForPortReady(t *testing.T) {
	h := newHarness(t)

	res := h.call("wait_for_port", map[string]any{"ports": []any{3000}})
	out := structured[mcpserver.WaitForPortOutput](t, res)
	if len(out.Ready) != 1 || out.Ready[0] != 3000 {
		t.Fatalf("ready = %v, want [3000]", out.Ready)
	}
	if len(out.TimedOut) != 0 {
		t.Errorf("timed_out = %v, want empty", out.TimedOut)
	}
	if out.ElapsedMs < 0 {
		t.Errorf("elapsed_ms = %d", out.ElapsedMs)
	}
	if text := textOf(res); !strings.Contains(text, "port 3000 is accepting connections") {
		t.Errorf("text = %q", text)
	}
}

func TestWaitForPortTimesOut(t *testing.T) {
	h := newHarness(t)

	res := h.call("wait_for_port", map[string]any{
		"ports":           []any{3000, 65000},
		"timeout_seconds": 1,
	})
	out := structured[mcpserver.WaitForPortOutput](t, res)
	if len(out.Ready) != 1 || out.Ready[0] != 3000 {
		t.Errorf("ready = %v, want [3000]", out.Ready)
	}
	if len(out.TimedOut) != 1 || out.TimedOut[0] != 65000 {
		t.Errorf("timed_out = %v, want [65000]", out.TimedOut)
	}
	text := textOf(res)
	if !strings.Contains(text, "timed out: 65000") {
		t.Errorf("text = %q, want it to name the port that never came up", text)
	}
}

// TestWaitForPortSeesAPortAppear is the readiness case that matters: the wait
// is already open when the port shows up, and the daemon's delta and the
// stream chunk both describe the same event.
func TestWaitForPortSeesAPortAppear(t *testing.T) {
	h := newHarness(t)

	type result struct {
		out  mcpserver.WaitForPortOutput
		text string
	}
	done := make(chan result, 1)
	go func() {
		res, err := h.client.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "wait_for_port",
			Arguments: map[string]any{"ports": []any{4321}, "timeout_seconds": 10},
		})
		if err != nil || res.IsError {
			done <- result{}
			return
		}
		var out mcpserver.WaitForPortOutput
		raw, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(raw, &out)
		done <- result{out: out, text: textOf(res)}
	}()

	// The port opens while the wait is in flight.
	appeared := state.Port{
		Port: 4321, BindAddress: "127.0.0.1", IPVersion: "IPv4",
		URL: "http://localhost:4321", PID: 5555, Process: "node",
		DisplayName: "worker", Type: state.TypeUser, User: "dev",
		ExposedURLs: []string{}, StartedAt: strPtr(fakedaemon.FixtureTime),
	}
	time.Sleep(20 * time.Millisecond)
	h.fake.SetPorts(append(fakedaemon.DefaultPorts(), appeared))
	h.fake.Push(state.Delta{Ports: state.Change[state.Port]{Added: []state.Port{appeared}}})

	select {
	case got := <-done:
		if len(got.out.Ready) != 1 || got.out.Ready[0] != 4321 {
			t.Fatalf("ready = %v, want [4321]", got.out.Ready)
		}
		if !strings.Contains(got.text, "port 4321 is accepting connections") {
			t.Errorf("text = %q", got.text)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("wait_for_port never returned")
	}
}

// TestWaitForPortHTTP is the difference the http argument buys: 5432 is bound
// but not an HTTP server, so a TCP wait succeeds where an HTTP wait does not.
func TestWaitForPortHTTP(t *testing.T) {
	h := newHarness(t)

	tcp := structured[mcpserver.WaitForPortOutput](t,
		h.call("wait_for_port", map[string]any{"ports": []any{5432}, "timeout_seconds": 1}))
	if len(tcp.Ready) != 1 {
		t.Errorf("a TCP wait on a bound port = %v, want it ready", tcp)
	}

	res := h.call("wait_for_port", map[string]any{
		"ports": []any{5432}, "timeout_seconds": 1, "http": true,
	})
	over := structured[mcpserver.WaitForPortOutput](t, res)
	if len(over.TimedOut) != 1 || over.TimedOut[0] != 5432 {
		t.Errorf("an HTTP wait on a non-http port = %v, want it timed out", over)
	}
	if text := textOf(res); !strings.Contains(text, "answering HTTP on /") {
		t.Errorf("text = %q, want it to say what readiness meant", text)
	}

	// A path is the third form of the argument.
	path := structured[mcpserver.WaitForPortOutput](t, h.call("wait_for_port", map[string]any{
		"ports": []any{3000}, "timeout_seconds": 2, "http": "/health",
	}))
	if len(path.Ready) != 1 || path.Ready[0] != 3000 {
		t.Errorf("an HTTP wait on a healthy port = %v, want it ready", path)
	}
}

func TestWaitForPortRejectsBadArguments(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name string
		args map[string]any
	}{
		{"no ports", map[string]any{"ports": []any{}}},
		{"not a port", map[string]any{"ports": []any{70000}}},
		{"too long", map[string]any{"ports": []any{3000}, "timeout_seconds": 600}},
		{"http is a number", map[string]any{"ports": []any{3000}, "http": 8080}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, ok := mcpserver.DecodeError(h.call("wait_for_port", tt.args))
			if !ok {
				t.Fatal("expected an error result")
			}
			if payload.Error.Code != mcpserver.CodeInvalidArguments {
				t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
			}
		})
	}
	if n := h.fake.Calls("ports.wait"); n != 0 {
		t.Errorf("the daemon was asked to wait %d times for argument errors", n)
	}
}

// ---------------------------------------------------------- next_free_port ---

func TestNextFreePort(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name string
		args map[string]any
		want []int
	}{
		{"default", map[string]any{}, []int{3001}},
		{"count", map[string]any{"count": 3}, []int{3001, 3002, 3003}},
		{"start", map[string]any{"start": 8080}, []int{8081}},
		{"range", map[string]any{"range": "5000-5500"}, []int{5000}},
		{"range over a listener", map[string]any{"range": "5173-5300"}, []int{5174}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := structured[rpc.PortsNextResult](t, h.call("next_free_port", tt.args))
			if !equalInts(out.Ports, tt.want) {
				t.Fatalf("ports = %v, want %v", out.Ports, tt.want)
			}
		})
	}

	if text := textOf(h.call("next_free_port", map[string]any{})); text != "port 3001 is free" {
		t.Errorf("text = %q", text)
	}

	payload, ok := mcpserver.DecodeError(h.call("next_free_port", map[string]any{"range": "3000..4000"}))
	if !ok {
		t.Fatal("a malformed range should be an error result")
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
}

// TestNextFreePortSkipsClaims is the claims half of contract §28 seen from the
// tools: a port this machine has claimed is not offered to the next caller.
func TestNextFreePortSkipsClaims(t *testing.T) {
	h := newHarnessWith(t, queryFixture("claims"))

	claimed := structured[mcpserver.ClaimPortOutput](t, h.call("claim_port", map[string]any{
		"project": "shop", "worktree": "main",
	}))
	if len(claimed.Ports) != 1 {
		t.Fatalf("claim_port returned %v, want one port", claimed.Ports)
	}
	port := claimed.Ports[0]

	out := structured[rpc.PortsNextResult](t, h.call("next_free_port", map[string]any{"start": port}))
	if len(out.Ports) != 1 || out.Ports[0] == port {
		t.Fatalf("next_free_port from %d = %v, want it to step over the claim", port, out.Ports)
	}
}

// -------------------------------------------------------------- claim_port ---

func TestClaimPortIsIdempotent(t *testing.T) {
	h := newHarnessWith(t, queryFixture("claims"))

	args := map[string]any{"project": "shop", "worktree": "feature-x", "count": 2}
	first := structured[mcpserver.ClaimPortOutput](t, h.call("claim_port", args))
	if first.Key != "shop/feature-x" {
		t.Errorf("key = %q, want shop/feature-x", first.Key)
	}
	if len(first.Ports) != 2 {
		t.Fatalf("ports = %v, want two", first.Ports)
	}
	if first.ExpiresAt == "" {
		t.Error("a claim without an expiry is not a claim")
	}

	second := structured[mcpserver.ClaimPortOutput](t, h.call("claim_port", args))
	if !equalInts(second.Ports, first.Ports) {
		t.Fatalf("claiming again returned %v, want the same %v", second.Ports, first.Ports)
	}

	text := textOf(h.call("claim_port", args))
	if !strings.HasPrefix(text, "shop/feature-x claims ") {
		t.Errorf("text = %q", text)
	}

	released := structured[mcpserver.ClaimPortOutput](t,
		h.call("claim_port", map[string]any{"project": "shop", "worktree": "feature-x", "release": true}))
	if released.Released != 2 {
		t.Errorf("released = %d, want 2", released.Released)
	}
	if len(released.Ports) != 0 {
		t.Errorf("a release returned ports: %v", released.Ports)
	}

	// Releasing again is a no-op, not a failure.
	again := structured[mcpserver.ClaimPortOutput](t,
		h.call("claim_port", map[string]any{"project": "shop", "worktree": "feature-x", "release": true}))
	if again.Released != 0 {
		t.Errorf("released = %d on the second release, want 0", again.Released)
	}
}

// TestClaimPortDefaultsToTheWorkingDirectory covers the identity the tool
// derives when the caller names nothing: the checkout the MCP server runs in.
func TestClaimPortDefaultsToTheWorkingDirectory(t *testing.T) {
	h := newHarnessWith(t, queryFixture("claims"))

	out := structured[mcpserver.ClaimPortOutput](t, h.call("claim_port", map[string]any{}))
	if !strings.Contains(out.Key, "/") {
		t.Fatalf("key = %q, want <project>/<worktree>", out.Key)
	}
	if len(out.Ports) != 1 {
		t.Fatalf("ports = %v, want one", out.Ports)
	}
}

func TestClaimPortWithoutTheCapability(t *testing.T) {
	h := newHarness(t) // the default fixture advertises no claims

	payload, ok := mcpserver.DecodeError(h.call("claim_port", map[string]any{"project": "shop"}))
	if !ok {
		t.Fatal("expected an error result")
	}
	if payload.Error.Code != mcpserver.CodeCapabilityMissing {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeCapabilityMissing)
	}
	if payload.Error.Hint == "" {
		t.Error("capability_missing must say what to do about it")
	}
	if n := h.fake.Calls("claims.acquire"); n != 0 {
		t.Errorf("the daemon was called %d times for a missing capability", n)
	}
}

// ---------------------------------------------------------------- tail_logs ---

func TestTailLogs(t *testing.T) {
	h := newHarness(t)

	res := h.call("tail_logs", map[string]any{"port": 3000})
	out := structured[mcpserver.TailLogsOutput](t, res)
	if len(out.Lines) != mcpserver.DefaultLogLines {
		t.Fatalf("got %d lines, want the default %d", len(out.Lines), mcpserver.DefaultLogLines)
	}
	if !out.Truncated {
		t.Error("a backlog longer than the request must be reported as truncated")
	}
	if out.Source == "" {
		t.Error("the reply must say where it read from")
	}
	if last := out.Lines[len(out.Lines)-1]; !strings.HasSuffix(last, "line 250") {
		t.Errorf("last line = %q, want the newest line", last)
	}
	if text := textOf(res); !strings.Contains(text, "100 lines from") || !strings.Contains(text, "line 250") {
		t.Errorf("text = %q", text)
	}

	// Asking for more than the process wrote is not truncation.
	whole := structured[mcpserver.TailLogsOutput](t,
		h.call("tail_logs", map[string]any{"port": 3000, "lines": 1000}))
	if len(whole.Lines) != 250 || whole.Truncated {
		t.Errorf("got %d lines, truncated=%v, want the whole 250-line backlog", len(whole.Lines), whole.Truncated)
	}
}

// TestTailLogsCapsTheRequest keeps the tool's own maximum: a request for more
// than MaxLogLines asks the daemon for the maximum and says so.
func TestTailLogsCapsTheRequest(t *testing.T) {
	h := newHarness(t)

	res := h.call("tail_logs", map[string]any{"port": 3000, "lines": 100000})
	if res.IsError {
		t.Fatalf("capping should not be an error: %s", textOf(res))
	}
	if text := textOf(res); !strings.Contains(text, "capped at 2000 lines") {
		t.Errorf("text = %q, want it to say the request was capped", text)
	}
}

func TestTailLogsSelectorRule(t *testing.T) {
	h := newHarness(t)

	for _, args := range []map[string]any{
		{},
		{"port": 3000, "pid": 4101},
		{"port": 3000, "run_id": "run-7f3a"},
	} {
		payload, ok := mcpserver.DecodeError(h.call("tail_logs", args))
		if !ok {
			t.Fatalf("args %v: expected an error result", args)
		}
		if payload.Error.Code != mcpserver.CodeInvalidArguments {
			t.Errorf("args %v: code = %q, want %q", args, payload.Error.Code, mcpserver.CodeInvalidArguments)
		}
	}
}

// TestTailLogsByRunID covers the resolution the daemon cannot do: ports.logs
// takes a port or a pid, so a run id is looked up in the run registry first.
func TestTailLogsByRunID(t *testing.T) {
	h := newHarness(t) // the default fixture advertises the run registry
	h.fake.Handle("runs.list", func(json.RawMessage) (any, error) {
		return rpc.RunsListResult{Runs: []rpc.RunRecord{{
			ID: "run-7f3a", PID: 4100, Group: "shop", Name: "api",
			Cmd: "node server.js", Cwd: "/home/dev/shop",
			StartedAt: fakedaemon.FixtureTime, Ports: []int{3000}, Status: "running",
		}}}, nil
	})

	out := structured[mcpserver.TailLogsOutput](t,
		h.call("tail_logs", map[string]any{"run_id": "run-7f3a", "lines": 5}))
	if len(out.Lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(out.Lines))
	}
	if !strings.Contains(out.Source, "api") {
		t.Errorf("source = %q, want the api's log", out.Source)
	}

	payload, ok := mcpserver.DecodeError(h.call("tail_logs", map[string]any{"run_id": "run-nope"}))
	if !ok {
		t.Fatal("an unknown run should be an error result")
	}
	if payload.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", payload.Error.Code)
	}
}

func TestTailLogsWithoutTheRunRegistry(t *testing.T) {
	h := newHarnessWith(t, fixtureWithout("runs"))

	payload, ok := mcpserver.DecodeError(h.call("tail_logs", map[string]any{"run_id": "run-7f3a"}))
	if !ok {
		t.Fatal("expected an error result")
	}
	if payload.Error.Code != mcpserver.CodeCapabilityMissing {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeCapabilityMissing)
	}
}

// ------------------------------------------------------------- health_check ---

func TestHealthCheckMapsTheDaemonsVerdict(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name   string
		port   int
		status string
		code   int
		reason string
	}{
		{"healthy", 3000, "ok", 200, "healthy"},
		{"bound but not http", 5432, "fail", 0, "non-http"},
		{"nothing listening", 9999, "fail", 0, "refused"},
		{"no probe yet", 22, "unknown", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := h.call("health_check", map[string]any{"port": tt.port})
			out := structured[mcpserver.HealthCheckOutput](t, res)
			if out.Status != tt.status {
				t.Errorf("status = %q, want %q", out.Status, tt.status)
			}
			if out.Code != tt.code {
				t.Errorf("code = %d, want %d", out.Code, tt.code)
			}
			if out.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", out.Reason, tt.reason)
			}
			if want := "http://localhost:" + strconv.Itoa(tt.port) + "/"; out.URL != want {
				t.Errorf("url = %q, want %q", out.URL, want)
			}
			if text := textOf(res); !strings.Contains(text, tt.status) {
				t.Errorf("text = %q, want it to carry the status", text)
			}
		})
	}
}

func TestHealthCheckRejectsAnotherPath(t *testing.T) {
	h := newHarness(t)

	payload, ok := mcpserver.DecodeError(h.call("health_check", map[string]any{"port": 3000, "path": "/ready"}))
	if !ok {
		t.Fatal("expected an error result")
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
	if !strings.Contains(payload.Error.Hint, "wait_for_port") {
		t.Errorf("hint = %q, want it to point at the tool that can probe a path", payload.Error.Hint)
	}
	// "/" is the supported path and must not be rejected.
	if res := h.call("health_check", map[string]any{"port": 3000, "path": "/"}); res.IsError {
		t.Errorf(`path "/" was rejected: %s`, textOf(res))
	}
}

// --------------------------------------------------------- dependency_graph ---

func TestDependencyGraphAggregatesEdges(t *testing.T) {
	h := newHarness(t)

	res := h.call("dependency_graph", map[string]any{})
	out := structured[mcpserver.DependencyGraphOutput](t, res)

	want := []mcpserver.DependencyEdge{
		{FromPort: 3000, ToPort: 5432, FromName: "api", ToName: "shop-db", Connections: 1},
		{FromPort: 5173, ToPort: 3000, FromName: "vite", ToName: "api", Connections: 2},
		{FromPort: 8080, ToPort: 3000, FromName: "shop-gateway", ToName: "api", Connections: 1},
	}
	if !jsonEqual(t, out.Edges, want) {
		t.Fatalf("edges =\n%s\nwant\n%s", mustJSON(t, out.Edges), mustJSON(t, want))
	}
	text := textOf(res)
	for _, want := range []string{"3 edges", "CONNECTIONS", "vite", "shop-db"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q:\n%s", want, text)
		}
	}
}

// ------------------------------------------------------------- port_history ---

func TestPortHistorySinceForms(t *testing.T) {
	h := newHarness(t)

	// The fixture's ring is one event an hour old, one two hours old and one
	// from the day before last.
	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{"default 24h", map[string]any{}, 2},
		{"duration", map[string]any{"since": "90m"}, 1},
		{"wider duration", map[string]any{"since": "48h"}, 3},
		{"timestamp", map[string]any{"since": time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)}, 2},
		{"port filter", map[string]any{"since": "48h", "port": 5432}, 1},
		{"limit", map[string]any{"since": "48h", "limit": 1}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := structured[rpc.PortsHistoryResult](t, h.call("port_history", tt.args))
			if len(out.Events) != tt.want {
				t.Fatalf("got %d events, want %d: %s", len(out.Events), tt.want, mustJSON(t, out.Events))
			}
		})
	}

	res := h.call("port_history", map[string]any{})
	if text := textOf(res); !strings.Contains(text, "since 24h") || !strings.Contains(text, "KIND") {
		t.Errorf("text = %q", text)
	}
}

// TestPortHistoryPassesTheSinceGrammarThrough keeps one owner for the grammar:
// the daemon rejects what it does not understand, and the tool reports its code.
func TestPortHistoryPassesTheSinceGrammarThrough(t *testing.T) {
	h := newHarness(t)

	payload, ok := mcpserver.DecodeError(h.call("port_history", map[string]any{"since": "last tuesday"}))
	if !ok {
		t.Fatal("expected an error result")
	}
	if payload.Error.Code != "invalid_params" {
		t.Errorf("code = %q, want the daemon's invalid_params", payload.Error.Code)
	}
}

// ------------------------------------------------------------ list_sessions ---

func TestListSessions(t *testing.T) {
	fx := queryFixture("sessions")
	fx.Sessions = append(fx.Sessions, state.SessionRecord{
		Session: state.Session{
			ID: "codex:1a2b", Tool: "codex", Label: "docs",
			Worktree: "docs-fix", Branch: "docs", Detected: true,
		},
		FirstSeen: fakedaemon.FixtureTime, LastSeen: fakedaemon.FixtureTime,
		Runs: 0, Ports: 0, Groups: 0, Active: false,
	})
	h := newHarnessWith(t, fx)

	res := h.call("list_sessions", map[string]any{})
	out := structured[rpc.SessionsListResult](t, res)
	if len(out.Sessions) != 1 || out.Sessions[0].ID != "claude-code:9f2c" {
		t.Fatalf("sessions = %s, want only the active one", mustJSON(t, out.Sessions))
	}
	if !jsonEqual(t, out.Sessions, fakedaemon.DefaultSessions()) {
		t.Errorf("structured content differs from sessions.list:\n%s", mustJSON(t, out.Sessions))
	}
	text := textOf(res)
	for _, want := range []string{"1 session", "claude-code:9f2c", "BRANCH", "ACTIVE"} {
		if !strings.Contains(text, want) {
			t.Errorf("text is missing %q:\n%s", want, text)
		}
	}

	all := structured[rpc.SessionsListResult](t,
		h.call("list_sessions", map[string]any{"active_only": false}))
	if len(all.Sessions) != 2 {
		t.Fatalf("active_only: false returned %d sessions, want 2", len(all.Sessions))
	}
}

func TestListSessionsWithoutTheCapability(t *testing.T) {
	h := newHarness(t)

	payload, ok := mcpserver.DecodeError(h.call("list_sessions", map[string]any{}))
	if !ok {
		t.Fatal("expected an error result")
	}
	if payload.Error.Code != mcpserver.CodeCapabilityMissing {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeCapabilityMissing)
	}
	if n := h.fake.Calls("sessions.list"); n != 0 {
		t.Errorf("the daemon was called %d times for a missing capability", n)
	}
}

func strPtr(s string) *string { return &s }

// TestWaitForPortCancelsTheStream is spec 2 §1's cancellation rule: when the
// MCP client drops the call, the daemon is told to stop waiting rather than
// left probing a port nobody is listening for any more.
func TestWaitForPortCancelsTheStream(t *testing.T) {
	h := newHarness(t)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.client.CallTool(ctx, &mcp.CallToolParams{
			Name:      "wait_for_port",
			Arguments: map[string]any{"ports": []any{65000}, "timeout_seconds": 300},
		})
	}()

	waitFor(t, 10*time.Second, "the wait to reach the daemon", func() bool { return h.fake.Calls("ports.wait") == 1 })
	cancel()
	waitFor(t, 10*time.Second, "the stream to be cancelled", func() bool { return h.fake.Calls("stream.cancel") >= 1 })
	<-done
}
