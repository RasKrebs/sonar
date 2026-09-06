package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// harness is a real MCP client talking over an in-memory transport to a real
// MCP server talking, over the real JSON-RPC codec, to a fake daemon. Only the
// port scan is fake; every layer under test is the shipped one.
type harness struct {
	t      *testing.T
	fake   *fakedaemon.Fake
	server *mcpserver.Server
	client *mcp.ClientSession
	logs   *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, fakedaemon.DefaultFixture())
}

func newHarnessWith(t *testing.T, fx fakedaemon.Fixture) *harness {
	t.Helper()

	fake := fakedaemon.New(fx)
	if err := fake.Start(); err != nil {
		t.Fatalf("starting the fake daemon: %v", err)
	}
	t.Cleanup(fake.Close)

	logs := &bytes.Buffer{}
	ctx := context.Background()
	server, err := mcpserver.New(ctx, mcpserver.Options{
		Version: "test",
		Logger:  mcpserver.NewLogger(logs, slog.LevelDebug, true),
		DaemonOptions: mcpserver.DaemonOptions{
			Socket:      fake.Addr(),
			NoAutostart: true,
			Timeout:     5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("starting the MCP server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("connecting the server transport: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return &harness{t: t, fake: fake, server: server, client: session, logs: logs}
}

// call runs a tool and fails the test if the call itself (as opposed to the
// tool) failed. A tool error comes back in the result, which is the point of
// the error model.
func (h *harness) call(name string, args map[string]any) *mcp.CallToolResult {
	h.t.Helper()
	res, err := h.client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

// structured decodes a successful result's structured content.
func structured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool reported an error: %s", textOf(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshalling structured content: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding structured content: %v\n%s", err, raw)
	}
	return out
}

func textOf(res *mcp.CallToolResult) string {
	var b bytes.Buffer
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsListAdvertisesTheReadTools(t *testing.T) {
	h := newHarness(t)

	res, err := h.client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	// Other slices add their own tools to the same server, so this asserts
	// what the read tools promise rather than how many tools exist.
	for _, name := range []string{"list_ports", "inspect_port"} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("tools/list is missing %s", name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", name)
		}
		if tool.Annotations == nil {
			t.Fatalf("%s has no annotations", name)
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only", name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s should be annotated closed-world: it only talks to the local daemon", name)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema", name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", name)
		}
	}
}

// TestListPortsMatchesPortsList is the golden test the step asks for: the tool's
// structured content is byte-identical to what `ports.list` returned.
func TestListPortsMatchesPortsList(t *testing.T) {
	h := newHarness(t)

	res := h.call("list_ports", map[string]any{})
	got := structured[map[string]any](t, res)

	want := map[string]any{"ports": toAny(t, visiblePorts(fakedaemon.DefaultPorts()))}
	if !jsonEqual(t, got, want) {
		t.Fatalf("structured content differs from ports.list\n got: %s\nwant: %s",
			mustJSON(t, got), mustJSON(t, want))
	}

	// The desktop app hides apps too: a default read must not carry the
	// ControlCenter row.
	for _, p := range got["ports"].([]any) {
		if p.(map[string]any)["is_app"] == true {
			t.Errorf("a default list_ports returned a desktop app: %v", p)
		}
	}
}

func TestListPortsFilters(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name  string
		args  map[string]any
		ports []int
	}{
		{"group", map[string]any{"group": "shop"}, []int{3000, 5173}},
		{"run name", map[string]any{"group": "api"}, []int{3000}},
		{"type docker", map[string]any{"type": "docker"}, []int{5432, 8080}},
		{"type system", map[string]any{"type": "system"}, []int{22}},
		{"include apps", map[string]any{"include_apps": true}, []int{3000, 5173, 5432, 8080, 22, 7000}},
		{"session", map[string]any{"session": "claude-code:9f2c"}, []int{3000}},
		{"session prefix", map[string]any{"session": "claude-code"}, []int{3000}},
		{"unknown session", map[string]any{"session": "codex:nope"}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := structured[struct {
				Ports []state.Port `json:"ports"`
			}](t, h.call("list_ports", tt.args))
			got := make([]int, 0, len(out.Ports))
			for _, p := range out.Ports {
				got = append(got, p.Port)
			}
			if !equalInts(got, tt.ports) {
				t.Fatalf("got ports %v, want %v", got, tt.ports)
			}
		})
	}
}

func TestListPortsStatsAreOptIn(t *testing.T) {
	h := newHarness(t)

	type out struct {
		Ports []state.Port `json:"ports"`
	}
	bare := structured[out](t, h.call("list_ports", map[string]any{}))
	for _, p := range bare.Ports {
		if p.Stats != nil {
			t.Fatalf("port %d carried stats without stats: true", p.Port)
		}
	}

	withStats := structured[out](t, h.call("list_ports", map[string]any{"stats": true}))
	found := false
	for _, p := range withStats.Ports {
		if p.Stats != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("stats: true returned no stats on any port")
	}
}

func TestListPortsRejectsAnUnknownType(t *testing.T) {
	h := newHarness(t)

	res := h.call("list_ports", map[string]any{"type": "container"})
	payload, ok := mcpserver.DecodeError(res)
	if !ok {
		t.Fatalf("expected an error result, got %s", textOf(res))
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
	if payload.Error.Hint == "" {
		t.Error("an invalid_arguments error must carry a hint")
	}
	// The daemon was never asked.
	if n := h.fake.Calls("ports.list"); n != 0 {
		t.Errorf("the daemon was called %d times for an argument error", n)
	}
}

func TestInspectPort(t *testing.T) {
	h := newHarness(t)

	out := structured[mcpserver.InspectPortOutput](t, h.call("inspect_port", map[string]any{"port": 3000}))
	if out.Port.Port != 3000 {
		t.Fatalf("inspected port %d, want 3000", out.Port.Port)
	}
	if out.Port.Cwd == "" || out.Port.ProjectRoot == nil || out.Port.Group == nil {
		t.Errorf("inspect_port dropped the fields it exists for: %+v", out.Port)
	}
	if out.Port.Session == nil {
		t.Error("inspect_port dropped the session")
	}

	text := textOf(h.call("inspect_port", map[string]any{"port": 3000}))
	for _, want := range []string{"port", "cwd", "group", "session", "health"} {
		if !bytes.Contains([]byte(text), []byte(want)) {
			t.Errorf("the text block omits %q:\n%s", want, text)
		}
	}
}

func TestInspectPortNotFound(t *testing.T) {
	h := newHarness(t)

	res := h.call("inspect_port", map[string]any{"port": 9999})
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
	if want := "not_found: "; !bytes.HasPrefix([]byte(textOf(res)), []byte(want)) {
		t.Errorf("text = %q, want it to start with %q", textOf(res), want)
	}
}

func TestInspectPortAmbiguous(t *testing.T) {
	fx := fakedaemon.DefaultFixture()
	fx.Ports = append(fx.Ports,
		state.Port{Port: 9229, BindAddress: "127.0.0.1", IPVersion: "IPv4", PID: 700, Type: state.TypeUser, ExposedURLs: []string{}},
		state.Port{Port: 9229, BindAddress: "::1", IPVersion: "IPv6", PID: 701, Type: state.TypeUser, ExposedURLs: []string{}},
	)
	h := newHarnessWith(t, fx)

	payload, ok := mcpserver.DecodeError(h.call("inspect_port", map[string]any{"port": 9229}))
	if !ok {
		t.Fatal("expected an error result for a port bound twice")
	}
	if payload.Error.Code != "ambiguous" {
		t.Errorf("code = %q, want ambiguous", payload.Error.Code)
	}

	// The pid argument is the way out of the ambiguity.
	out := structured[mcpserver.InspectPortOutput](t,
		h.call("inspect_port", map[string]any{"port": 9229, "pid": 701}))
	if out.Port.BindAddress != "::1" {
		t.Errorf("pid 701 resolved to %s, want the ::1 row", out.Port.BindAddress)
	}
}

func TestInspectPortRejectsAPidOnAnotherPort(t *testing.T) {
	h := newHarness(t)

	payload, ok := mcpserver.DecodeError(h.call("inspect_port", map[string]any{"port": 8080, "pid": 4101}))
	if !ok {
		t.Fatal("expected an error when the pid does not own the port")
	}
	if payload.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", payload.Error.Code)
	}
}

func TestInspectPortNeedsASelector(t *testing.T) {
	h := newHarness(t)

	payload, ok := mcpserver.DecodeError(h.call("inspect_port", map[string]any{"port": 0}))
	if !ok {
		t.Fatal("expected an error for a zero port")
	}
	if payload.Error.Code != mcpserver.CodeInvalidArguments {
		t.Errorf("code = %q, want %q", payload.Error.Code, mcpserver.CodeInvalidArguments)
	}
}

func visiblePorts(all []state.Port) []state.Port {
	out := []state.Port{}
	for _, p := range all {
		if p.IsApp {
			continue
		}
		p.Stats = nil
		p.Health = nil
		out = append(out, p)
	}
	return out
}

func toAny(t *testing.T, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(mustJSON(t, v), &out); err != nil {
		t.Fatalf("round-tripping %T: %v", v, err)
	}
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %T: %v", v, err)
	}
	return raw
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	return bytes.Equal(mustJSON(t, a), mustJSON(t, b))
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
