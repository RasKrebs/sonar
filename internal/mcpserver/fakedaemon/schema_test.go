package fakedaemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// TestRepliesValidateAgainstTheProtocolSchema is what makes the fake worth
// having: everything it answers is checked against the generated
// docs/schema/protocol.schema.json, so a test that passes against the fake is
// testing the published contract and not a mock that drifted away from it.
func TestRepliesValidateAgainstTheProtocolSchema(t *testing.T) {
	c := connect(t, fakedaemon.DefaultFixture())

	cases := []struct {
		method string
		params any
		def    string
	}{
		{"daemon.hello", rpc.DaemonHelloParams{Client: "mcp", ClientVersion: "test"}, "DaemonHelloResult"},
		{"daemon.status", rpc.Empty{}, "DaemonStatusResult"},
		{"state.snapshot", rpc.StateSnapshotParams{}, "Snapshot"},
		{"state.subscribe", rpc.StateSubscribeParams{}, "Snapshot"},
		{"ports.list", rpc.PortsListParams{All: true}, "PortsListResult"},
		{"ports.list", rpc.PortsListParams{Include: rpc.Include{"stats", "health"}}, "PortsListResult"},
		{"ports.inspect", rpc.Selector{Port: intp(3000)}, "PortsInspectResult"},
		{"groups.list", rpc.Empty{}, "GroupsListResult"},
		{"ports.next", rpc.PortsNextParams{Start: 3001}, "PortsNextResult"},
		{"ports.health", rpc.PortsHealthParams{Ports: []int{3000, 9999}}, "PortsHealthResult"},
		{"ports.logs", rpc.PortsLogsParams{Selector: rpc.Selector{Port: intp(3000)}}, "PortsLogsResult"},
		{"ports.graph", rpc.Empty{}, "PortsGraphResult"},
		{"ports.history", rpc.PortsHistoryParams{Since: strp("48h")}, "PortsHistoryResult"},
		{"sessions.list", rpc.SessionsListParams{}, "SessionsListResult"},
		{"claims.acquire", rpc.ClaimsAcquireParams{Project: "shop"}, "ClaimsAcquireResult"},
		{"claims.list", rpc.Empty{}, "ClaimsListResult"},
		{"claims.release", rpc.ClaimsReleaseParams{Key: "shop/main"}, "ClaimsReleaseResult"},
		{"groups.inspect", rpc.GroupsInspectParams{Name: "shop"}, "GroupsInspectResult"},
	}

	for _, tc := range cases {
		t.Run(tc.method+"/"+tc.def, func(t *testing.T) {
			var raw json.RawMessage
			if err := c.Call(t.Context(), tc.method, tc.params, &raw); err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			validate(t, tc.def, raw)
		})
	}
}

// TestWaitStreamValidates covers the streaming half: ports.wait chunks and its
// end payload are the shapes the contract publishes, and the stream ends even
// when the client cancels it.
func TestWaitStreamValidates(t *testing.T) {
	c := connect(t, fakedaemon.DefaultFixture())

	st, err := c.Stream(t.Context(), "ports.wait", rpc.PortsWaitParams{
		Ports: []int{3000, 65000}, TimeoutMs: 200,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	chunks := 0
	for raw := range st.Chunks() {
		validate(t, "PortsWaitChunk", raw)
		chunks++
	}
	if chunks != 1 {
		t.Errorf("got %d chunks, want one for the listening port", chunks)
	}
	end := <-st.End()
	if end.Err != nil {
		t.Fatalf("the stream ended with an error: %v", end.Err)
	}
	validate(t, "PortsWaitEnd", end.Data)

	var payload rpc.PortsWaitEnd
	if err := end.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Ready) != 1 || payload.Ready[0] != 3000 {
		t.Errorf("ready = %v, want [3000]", payload.Ready)
	}
	if len(payload.TimedOut) != 1 || payload.TimedOut[0] != 65000 {
		t.Errorf("timed_out = %v, want [65000]", payload.TimedOut)
	}
}

// TestWaitStreamCancels is contract §20: a cancelled stream still ends, with
// no error and with whatever it had.
func TestWaitStreamCancels(t *testing.T) {
	c := connect(t, fakedaemon.DefaultFixture())

	st, err := c.Stream(t.Context(), "ports.wait", rpc.PortsWaitParams{
		Ports: []int{65000}, TimeoutMs: 60_000,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	if err := st.Cancel(t.Context()); err != nil {
		t.Fatalf("stream.cancel: %v", err)
	}
	select {
	case end := <-st.End():
		if end.Err != nil {
			t.Fatalf("a cancelled stream ended with an error: %v", end.Err)
		}
		validate(t, "PortsWaitEnd", end.Data)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled stream never ended")
	}
}

// TestDeltasValidate covers the notification half: a pushed state.delta is the
// shape a subscriber decodes.
func TestDeltasValidate(t *testing.T) {
	fake := start(t, fakedaemon.DefaultFixture())
	c := dial(t, fake)

	sub, err := c.Subscribe(t.Context(), client.SubscribeOptions{})
	if err != nil {
		t.Fatalf("subscribing: %v", err)
	}

	fake.Push(state.Delta{Ports: state.Change[state.Port]{
		Added:   fakedaemon.ManyPorts(1),
		Updated: []state.Port{},
		Removed: []string{"22:0.0.0.0"},
	}})

	select {
	case delta := <-sub.Deltas:
		raw, err := json.Marshal(delta)
		if err != nil {
			t.Fatal(err)
		}
		validate(t, "Delta", raw)
		if delta.Seq == 0 {
			t.Error("a pushed delta should carry a sequence number")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delta arrived")
	}
}

// TestUnknownMethodIsNotFound pins the fake's error vocabulary to the contract
// §2 registry: there is no method_not_found (contract §21).
func TestUnknownMethodIsNotFound(t *testing.T) {
	c := connect(t, fakedaemon.DefaultFixture())

	err := c.Call(t.Context(), "ports.teleport", rpc.Empty{}, nil)
	if err == nil {
		t.Fatal("an unknown method should fail")
	}
	rerr, ok := err.(*rpc.Error)
	if !ok {
		t.Fatalf("got %T, want *rpc.Error", err)
	}
	if rerr.Code != rpc.CodeNotFound || rerr.Data.Code != "not_found" {
		t.Errorf("got %d/%s, want %d/not_found", rerr.Code, rerr.Data.Code, rpc.CodeNotFound)
	}
}

func TestPortsListFiltersMatchTheDaemon(t *testing.T) {
	c := connect(t, fakedaemon.DefaultFixture())

	tests := []struct {
		name   string
		params rpc.PortsListParams
		want   []int
	}{
		{"default hides apps", rpc.PortsListParams{}, []int{3000, 5173, 5432, 8080, 22}},
		{"all", rpc.PortsListParams{All: true}, []int{3000, 5173, 5432, 8080, 22, 7000}},
		{"docker", rpc.PortsListParams{Filter: strp("docker")}, []int{5432, 8080}},
		{"group", rpc.PortsListParams{Group: strp("shop-infra")}, []int{5432, 8080}},
		{"run id", rpc.PortsListParams{Group: strp("run-7f3a")}, []int{3000}},
		{"ipv6", rpc.PortsListParams{IPVersion: strp("6")}, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res rpc.PortsListResult
			if err := c.Call(t.Context(), "ports.list", tt.params, &res); err != nil {
				t.Fatal(err)
			}
			got := []int{}
			for _, p := range res.Ports {
				got = append(got, p.Port)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}

	var bad rpc.PortsListResult
	err := c.Call(t.Context(), "ports.list", rpc.PortsListParams{Filter: strp("nope")}, &bad)
	if err == nil {
		t.Fatal("an unknown filter should fail")
	}
}

func start(t *testing.T, fx fakedaemon.Fixture) *fakedaemon.Fake {
	t.Helper()
	fake := fakedaemon.New(fx)
	if err := fake.Start(); err != nil {
		t.Fatalf("starting the fake: %v", err)
	}
	t.Cleanup(fake.Close)
	return fake
}

func dial(t *testing.T, fake *fakedaemon.Fake) *client.Client {
	t.Helper()
	c, err := client.Dial(context.Background(), client.ClientInfo{
		Name: "mcp", Version: "test", Socket: fake.Addr(),
	})
	if err != nil {
		t.Fatalf("dialling the fake: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func connect(t *testing.T, fx fakedaemon.Fixture) *client.Client {
	t.Helper()
	return dial(t, start(t, fx))
}

// validate checks raw against a named definition of the generated protocol
// schema.
func validate(t *testing.T, def string, raw json.RawMessage) {
	t.Helper()
	path := filepath.Join("..", "..", "..", "docs", "schema", "protocol.schema.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	defer f.Close()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("protocol.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := compiler.Compile("protocol.schema.json#/definitions/" + def)
	if err != nil {
		t.Fatalf("compiling #/definitions/%s: %v", def, err)
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(decoded); err != nil {
		t.Errorf("the reply does not validate against #/definitions/%s:\n%v\n%s", def, err, raw)
	}
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }
