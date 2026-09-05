package daemon

import (
	"context"
	"net"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/killer"
	"github.com/raskrebs/sonar/internal/state"
)

func ptr[T any](v T) *T { return &v }

// snapshotWith builds the snapshot the kill handlers resolve against.
func snapshotWith(pp ...state.Port) state.Snapshot {
	return state.Snapshot{Ports: pp}
}

func TestSelectorTargetRequiresExactlyOneKey(t *testing.T) {
	snap := snapshotWith()
	cases := map[string]rpc.Selector{
		"empty":     {},
		"two keys":  {Port: ptr(3000), PID: ptr(42)},
		"bind only": {BindAddress: ptr("127.0.0.1")},
	}
	for name, sel := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := selectorTarget(sel, snap); err == nil {
				t.Fatal("want an invalid_selector error")
			} else if killer.Code(err) != killer.CodeInvalidSelector {
				t.Fatalf("code = %q, want invalid_selector", killer.Code(err))
			}
		})
	}
}

func TestSelectorTargetPinsAnUnambiguousBindAddress(t *testing.T) {
	snap := snapshotWith(state.Port{Port: 3000, BindAddress: "127.0.0.1"})
	got, err := selectorTarget(rpc.Selector{Port: ptr(3000)}, snap)
	if err != nil {
		t.Fatalf("selectorTarget: %v", err)
	}
	if got.Port != 3000 || got.BindAddress != "127.0.0.1" {
		t.Fatalf("target = %+v, want the snapshot's bind address", got)
	}
}

func TestSelectorTargetLeavesAMultiBindPortOpen(t *testing.T) {
	snap := snapshotWith(
		state.Port{Port: 3000, BindAddress: "127.0.0.1"},
		state.Port{Port: 3000, BindAddress: "::1"},
	)
	got, err := selectorTarget(rpc.Selector{Port: ptr(3000)}, snap)
	if err != nil {
		t.Fatalf("selectorTarget: %v", err)
	}
	if got.BindAddress != "" {
		t.Fatalf("bind_address = %q, want it left to the killer to report as ambiguous", got.BindAddress)
	}
}

func TestSelectorTargetCarriesTheOtherSelectorKeys(t *testing.T) {
	snap := snapshotWith()
	if got, err := selectorTarget(rpc.Selector{PID: ptr(4242)}, snap); err != nil || got.PID != 4242 {
		t.Fatalf("pid selector = %+v, %v", got, err)
	}
	if got, err := selectorTarget(rpc.Selector{RunID: ptr("run-1")}, snap); err != nil || got.RunID != "run-1" {
		t.Fatalf("run_id selector = %+v, %v", got, err)
	}
	if got, err := selectorTarget(rpc.Selector{ProxyID: ptr("px-1")}, snap); err != nil || got.ProxyID != "px-1" {
		t.Fatalf("proxy_id selector = %+v, %v", got, err)
	}
}

func TestGroupTargetsMatchesGroupRunAndComposeProject(t *testing.T) {
	snap := snapshotWith(
		state.Port{Port: 3000, BindAddress: "127.0.0.1", Group: ptr("my-app")},
		state.Port{Port: 3001, BindAddress: "127.0.0.1", Run: &state.Run{Group: "MY-APP"}},
		state.Port{Port: 5432, BindAddress: "0.0.0.0", Docker: &state.Docker{ComposeProject: "my-app"}},
		state.Port{Port: 9999, BindAddress: "127.0.0.1", Group: ptr("other")},
	)
	got := groupTargets(snap, "my-app")
	if len(got) != 3 {
		t.Fatalf("targets = %+v, want the three my-app rows", got)
	}
	for _, tg := range got {
		if tg.Port == 9999 {
			t.Fatalf("group kill picked up an unrelated port: %+v", got)
		}
	}
}

func TestKillEnvelopeListsAffectedKeysOnce(t *testing.T) {
	env := killEnvelope([]state.KillResult{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 400, Method: state.MethodSIGTERM, OK: true},
		{Port: 3000, BindAddress: "127.0.0.1", PID: 300, Method: state.MethodSIGKILL, OK: true},
		{Port: 0, PID: 12345, Method: state.MethodSIGTERM, OK: true},
	})
	if !env.OK {
		t.Error("ok = false, want true when every row succeeded")
	}
	if len(env.Affected) != 1 || env.Affected[0] != "3000:127.0.0.1" {
		t.Errorf("affected = %v, want one port key", env.Affected)
	}
}

func TestKillEnvelopeIsNotOKWhenARowFailed(t *testing.T) {
	env := killEnvelope([]state.KillResult{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 400, Method: state.MethodSIGTERM, OK: true},
		{Port: 8080, BindAddress: "127.0.0.1", Method: state.MethodNone, OK: false, Error: "boom"},
	})
	if env.OK {
		t.Error("ok = true, want false when a row failed")
	}
	if len(env.Affected) != 1 || env.Affected[0] != "3000:127.0.0.1" {
		t.Errorf("affected = %v, want only the successful row", env.Affected)
	}
}

func TestKillEnvelopeAlwaysHasArrays(t *testing.T) {
	env := killEnvelope(nil)
	if env.Results == nil || env.Affected == nil {
		t.Fatalf("envelope = %+v, want empty arrays rather than null", env)
	}
}

func TestPortsKillRejectsEmptyTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	e := c.call("ports.kill", rpc.PortsKillParams{}, nil)
	if e == nil {
		t.Fatal("ports.kill with no targets succeeded")
	}
	if e.Code != rpc.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", e.Code, rpc.CodeInvalidParams)
	}
}

func TestPortsKillRejectsAnInvalidSelector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	e := c.call("ports.kill", rpc.PortsKillParams{Targets: []rpc.Selector{{}}}, nil)
	if e == nil {
		t.Fatal("ports.kill with an empty selector succeeded")
	}
	if e.Code != rpc.CodeInvalidSelector || e.Data.Code != "invalid_selector" {
		t.Fatalf("error = %d/%q, want 1009/invalid_selector", e.Code, e.Data.Code)
	}
}

func TestPortsKillDryRunReportsAnUnknownPortAsAFailedRow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	port := freeTestPort(t)
	var env rpc.KillEnvelope
	if e := c.call("ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: ptr(port)}}, DryRun: true,
	}, &env); e != nil {
		t.Fatalf("ports.kill: %v", e)
	}
	if env.OK {
		t.Errorf("ok = true, want false for a port nothing listens on")
	}
	if len(env.Results) != 1 || env.Results[0].Method != state.MethodNone || env.Results[0].OK {
		t.Fatalf("results = %+v, want one failed none row", env.Results)
	}
	if len(env.Affected) != 0 {
		t.Errorf("affected = %v, want nothing affected", env.Affected)
	}
}

func TestGroupsKillReportsAnUnknownGroupAsNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	e := c.call("groups.kill", rpc.GroupsKillParams{Name: "no-such-group"}, nil)
	if e == nil {
		t.Fatal("groups.kill on an unknown group succeeded")
	}
	if e.Code != rpc.CodeNotFound || e.Data.Code != "not_found" {
		t.Fatalf("error = %d/%q, want 1001/not_found", e.Code, e.Data.Code)
	}
	if e.Data.Hint == "" {
		t.Error("hint is empty; contract §2 asks for an actionable one")
	}
}

func TestGroupsKillRequiresAName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	if e := c.call("groups.kill", rpc.GroupsKillParams{Name: "  "}, nil); e == nil ||
		e.Code != rpc.CodeInvalidParams {
		t.Fatalf("error = %v, want invalid_params", e)
	}
}

func TestKillCapabilityIsAdvertised(t *testing.T) {
	for _, c := range Capabilities() {
		if c == "ports.kill" {
			return
		}
	}
	t.Fatalf("capabilities = %v, missing ports.kill", Capabilities())
}

// freeTestPort reserves a port and releases it, so nothing is listening on it.
func freeTestPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
