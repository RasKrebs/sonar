package daemon

import (
	"context"
	"net"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/killer"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
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

// TestKillResolvesAgainstTheDaemonsOwnScan: the handler hands the killer the
// rows it just scanned, instead of letting killer.KillPorts scan the machine a
// second time inside the RPC. The proof is a listener that exists only in the
// daemon's scan: a killer that went looking for itself would find nothing on
// that port and report a failed "none" row, so a planned signal carrying the
// snapshot's own pid and name is the daemon's scan being used.
func TestKillResolvesAgainstTheDaemonsOwnScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	const fakePID = 424242
	port := freeTestPort(t)
	h.setRows(ports.ListeningPort{
		Port: port, BindAddress: "127.0.0.1", PID: fakePID,
		Process: "node", Display: "fake-api",
	})

	var env rpc.KillEnvelope
	if e := c.call("ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: ptr(port)}}, DryRun: true,
	}, &env); e != nil {
		t.Fatalf("ports.kill: %v", e)
	}
	if len(env.Results) != 1 {
		t.Fatalf("results = %+v, want one row for the snapshot's listener", env.Results)
	}
	row := env.Results[0]
	if row.Method == state.MethodNone {
		t.Fatalf("row = %+v, want a planned signal: the killer resolved against its own scan, not the daemon's", row)
	}
	if row.PID != fakePID || row.Name != "fake-api" || row.Port != port {
		t.Fatalf("row = %+v, want the daemon snapshot's pid %d and name fake-api on port %d",
			row, fakePID, port)
	}
}

// TestKillCostsExactlyOneScan pins the other half: resolving targets rescans
// once (contract §25) and nothing scans again behind it.
func TestKillCostsExactlyOneScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	h.setRows(ports.ListeningPort{Port: 4323, BindAddress: "127.0.0.1", PID: 424243, Process: "node"})
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before := h.loop.Status().Scans

	var env rpc.KillEnvelope
	if e := c.call("ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: ptr(4323)}}, DryRun: true,
	}, &env); e != nil {
		t.Fatalf("ports.kill: %v", e)
	}
	if after := h.loop.Status().Scans; after != before+1 {
		t.Errorf("scans = %d, want exactly one (target resolution) more than %d", after, before)
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

// TestKillRepublishesSoTheNextReadIsFresh: a kill drops the cached scan, so the
// caller's own next `sonar list` does not still show the port it just freed,
// and the port_down row reaches the history ring with nobody subscribed
// (step 1A.7).
func TestKillRepublishesSoTheNextReadIsFresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	h.setRows(ports.ListeningPort{Port: 4321, BindAddress: "127.0.0.1", PID: 999, Process: "node"})
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before := h.loop.Status().Scans

	// The kill itself fails (there is no pid 999), which is beside the point:
	// the handler still has to leave the cache invalidated.
	h.setRows()
	var env rpc.KillEnvelope
	if e := c.call("ports.kill", rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(4321)}}}, &env); e != nil {
		t.Fatalf("ports.kill: %v", e)
	}
	if after := h.loop.Status().Scans; after <= before {
		t.Fatalf("scans = %d, want a rescan after the kill (was %d)", after, before)
	}
	if snap := h.loop.Cached(); len(snap.Ports) != 0 {
		t.Errorf("the cached snapshot still carries %d ports after the kill", len(snap.Ports))
	}
}

// TestGroupsKillResolvesAgainstAFreshScan: a group gaining a service inside the
// cache window must still be killed whole. `sonar up` starts three services in
// well under CacheTTL, and resolving `sonar kill -g` against the cached
// snapshot killed only the ones the last tick happened to see.
func TestGroupsKillResolvesAgainstAFreshScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	inDemo := func(port int) ports.ListeningPort {
		return ports.ListeningPort{
			Port: port, BindAddress: "127.0.0.1", PID: 900 + port, Process: "listener",
			RunID: "run-1", Tag: "svc", RunGroup: "demo",
		}
	}

	h.setRows(inDemo(4331))
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}

	// A second service of the same group comes up right after that scan.
	h.setRows(inDemo(4331), inDemo(4332))

	var env rpc.KillEnvelope
	if e := c.call("groups.kill", rpc.GroupsKillParams{Name: "demo", DryRun: true}, &env); e != nil {
		t.Fatalf("groups.kill: %v", e)
	}
	if len(env.Results) != 2 {
		t.Fatalf("results = %+v, want a row for each of the group's two ports", env.Results)
	}
	seen := map[int]bool{}
	for _, r := range env.Results {
		seen[r.Port] = true
	}
	if !seen[4331] || !seen[4332] {
		t.Fatalf("killed ports %v, want both 4331 and 4332", seen)
	}
}

// TestDryRunKillDoesNotRepublish: resolving the targets costs one scan, and a
// dry run stops there — nothing changed, so nothing needs republishing.
func TestDryRunKillDoesNotRepublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	h.setRows(ports.ListeningPort{Port: 4322, BindAddress: "127.0.0.1", PID: 998, Process: "node"})
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming: %v", err)
	}
	before := h.loop.Status().Scans

	var env rpc.KillEnvelope
	if e := c.call("ports.kill", rpc.PortsKillParams{
		Targets: []rpc.Selector{{Port: ptr(4322)}}, DryRun: true,
	}, &env); e != nil {
		t.Fatalf("ports.kill: %v", e)
	}
	if after := h.loop.Status().Scans; after != before+1 {
		t.Errorf("scans = %d, want exactly one (target resolution) more than %d", after, before)
	}
}
