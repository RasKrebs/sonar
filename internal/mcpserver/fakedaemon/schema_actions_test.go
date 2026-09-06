package fakedaemon_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
)

// TestActionRepliesValidateAgainstTheProtocolSchema is the read half's test
// applied to the write half: every reply the action methods produce is checked
// against docs/schema/protocol.schema.json, so a tool tested against the fake
// is tested against the published contract.
func TestActionRepliesValidateAgainstTheProtocolSchema(t *testing.T) {
	fake := start(t, fakedaemon.DefaultFixture())
	fake.RegisterActions()
	c := dial(t, fake)

	cases := []struct {
		method string
		params any
		def    string
	}{
		{"ports.kill", rpc.PortsKillParams{Targets: []rpc.Selector{{Port: intp(3000)}}, Tree: true, DryRun: true}, "KillEnvelope"},
		{"ports.kill", rpc.PortsKillParams{Targets: []rpc.Selector{{Port: intp(5432)}}, DryRun: true}, "KillEnvelope"},
		{"groups.kill", rpc.GroupsKillParams{Name: "shop", DryRun: true}, "KillEnvelope"},
		{"sessions.kill", rpc.SessionsKillParams{ID: "claude-code:9f2c", DryRun: true}, "KillEnvelope"},
		{"ports.rename", rpc.PortsRenameParams{Selector: rpc.Selector{Port: intp(5173)}, Name: strp("storefront")}, "PortsRenameResult"},
		{"runs.spawn", rpc.RunsSpawnParams{Argv: []string{"npm", "run", "dev"}, Cwd: "/home/dev/shop"}, "RunsSpawnResult"},
		{"runs.list", rpc.Empty{}, "RunsListResult"},
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

// TestPortsWaitStreamsLikeTheDaemon: the reply carries a subscription id, the
// chunks and the end are the contract §1 envelope, and the end's payload is
// PortsWaitEnd (contract §20).
func TestPortsWaitStreamsLikeTheDaemon(t *testing.T) {
	fake := start(t, fakedaemon.DefaultFixture())
	actions := fake.RegisterActions()
	c := dial(t, fake)

	var spawned rpc.RunsSpawnResult
	if err := c.Call(t.Context(), "runs.spawn", rpc.RunsSpawnParams{
		Argv: []string{"python3", "-m", "http.server", "8000"}, Cwd: "/home/dev/shop",
	}, &spawned); err != nil {
		t.Fatalf("runs.spawn: %v", err)
	}

	stream, err := c.Stream(t.Context(), "ports.wait", rpc.PortsWaitParams{
		Ports: []int{8000}, TimeoutMs: 5000, IntervalMs: 20,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	if stream.ID() == "" {
		t.Fatal("the reply carried no subscription id")
	}
	time.AfterFunc(50*time.Millisecond, func() { actions.OpenPortForRun(spawned.RunID, 8000) })

	var chunks []rpc.PortsWaitChunk
	for raw := range stream.Chunks() {
		var chunk rpc.PortsWaitChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatalf("decoding a chunk: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	end := <-stream.End()
	if end.Err != nil {
		t.Fatalf("the stream ended with an error: %v", end.Err)
	}
	var out rpc.PortsWaitEnd
	if err := end.Decode(&out); err != nil {
		t.Fatalf("decoding stream.end: %v", err)
	}

	if len(chunks) != 1 || chunks[0].Port != 8000 {
		t.Errorf("chunks = %+v, want one for port 8000", chunks)
	}
	if len(out.Ready) != 1 || out.Ready[0] != 8000 || len(out.TimedOut) != 0 {
		t.Errorf("end = %+v, want ready 8000", out)
	}
}

func TestPortsWaitTimesOut(t *testing.T) {
	fake := start(t, fakedaemon.DefaultFixture())
	fake.RegisterActions()
	c := dial(t, fake)

	stream, err := c.Stream(t.Context(), "ports.wait", rpc.PortsWaitParams{
		Ports: []int{8100}, TimeoutMs: 100, IntervalMs: 20,
	}, nil)
	if err != nil {
		t.Fatalf("ports.wait: %v", err)
	}
	for range stream.Chunks() {
	}
	var out rpc.PortsWaitEnd
	if err := (<-stream.End()).Decode(&out); err != nil {
		t.Fatalf("decoding stream.end: %v", err)
	}
	if len(out.Ready) != 0 || len(out.TimedOut) != 1 || out.TimedOut[0] != 8100 {
		t.Errorf("end = %+v, want 8100 timed out", out)
	}
}

// TestKillRemovesWhatItKilled is what makes the fake usable for a two-step
// test: a kill is visible to the next read, as the daemon's rescan makes it.
func TestKillRemovesWhatItKilled(t *testing.T) {
	fake := start(t, fakedaemon.DefaultFixture())
	fake.RegisterActions()
	c := dial(t, fake)

	var env rpc.KillEnvelope
	if err := c.Call(t.Context(), "ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: intp(5173)}},
	}, &env); err != nil {
		t.Fatalf("ports.kill: %v", err)
	}
	if !env.OK || len(env.Affected) != 1 {
		t.Fatalf("envelope = %+v, want one affected key", env)
	}

	var list rpc.PortsListResult
	if err := c.Call(t.Context(), "ports.list", rpc.PortsListParams{}, &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Ports {
		if p.Port == 5173 {
			t.Fatal("the killed port is still listed")
		}
	}
}
