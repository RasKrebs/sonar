package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// statsRows is a port set that already carries stats, the way a real scan's
// does once a subscriber has asked for them.
func statsRows() []ports.ListeningPort {
	return []ports.ListeningPort{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 100, Process: "node",
			CPUPercent: 1, MemoryRSS: 1 << 20, ThreadCount: 4, Connections: 2, State: "running"},
		{Port: 5173, BindAddress: "127.0.0.1", PID: 200, Process: "vite",
			CPUPercent: 2, MemoryRSS: 2 << 20, ThreadCount: 9, Connections: 1, State: "running"},
	}
}

// The step's acceptance test: a subscriber that asked for stats is fed by the
// 1 s stats tick, not by the adaptive port scan, so it sees several
// stats-bearing deltas in a few seconds even though no port changed.
func TestStatsSubscriberSeesASecondlyCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t, ctx)
	h.setRows(statsRows()...)
	h.statsMove.Store(true)

	c := h.dial(ctx)
	c.subscribeAndSettle(rpc.StateSubscribeParams{Include: rpc.Include{"stats"}})

	const want = 3
	deadline := time.Now().Add(5 * time.Second)
	var (
		seen  int
		times []time.Time
		last  uint64
	)
	for seen < want && time.Now().Before(deadline) {
		d := c.nextDelta()
		if d.Seq <= last {
			t.Fatalf("delta seq %d after %d: seq order is not publish order", d.Seq, last)
		}
		last = d.Seq
		if !carriesStats(d) {
			continue
		}
		// Contract §21: an updated row is the full Port object, not a patch.
		for _, p := range d.Ports.Updated {
			if p.Port == 0 || p.Process == "" {
				t.Fatalf("a stats delta carried a partial port: %+v", p)
			}
		}
		if len(d.Ports.Added)+len(d.Ports.Removed) != 0 {
			t.Fatalf("a stats tick added or removed a port: %+v", d.Ports)
		}
		seen++
		times = append(times, time.Now())
	}
	if seen < want {
		t.Fatalf("saw %d stats-bearing deltas in 5s, want at least %d", seen, want)
	}
	t.Logf("stats deltas at %v", times)
}

// A subscriber that asked for neither stats nor health still sees the machine
// load the same tick collects — host load is state, not an opt-in statistic
// (contract §37) — and never sees a `stats` object.
func TestNonStatsSubscriberGetsHostRowsButNoStats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t, ctx)
	h.setRows(statsRows()...)
	h.statsMove.Store(true)

	// One subscriber asks for stats, so the scanner really is collecting
	// them: the filter has to be what keeps them off the other's wire.
	statsClient := h.dial(ctx)
	statsClient.subscribeAndSettle(rpc.StateSubscribeParams{Include: rpc.Include{"stats"}})
	go drain(ctx, statsClient)

	plain := h.dial(ctx)
	plain.subscribeAndSettle(rpc.StateSubscribeParams{})

	hostDeltas := 0
	deadline := time.Now().Add(5 * time.Second)
	for hostDeltas < 3 && time.Now().Before(deadline) {
		d := plain.nextDelta()
		for _, p := range append(append([]state.Port{}, d.Ports.Added...), d.Ports.Updated...) {
			if p.Stats != nil {
				t.Fatalf("a subscriber without include:[stats] received stats: %+v", p.Stats)
			}
		}
		if len(d.Hosts.Updated)+len(d.Hosts.Added) > 0 {
			hostDeltas++
		}
	}
	if hostDeltas < 3 {
		t.Fatalf("saw %d host deltas in 5s, want at least 3", hostDeltas)
	}
}

// With nobody subscribed the stats tick is parked, so an RPC read — which
// wakes the loop — must not start it and `daemon.status` must report exactly
// what a scan left behind.
func TestStatsTickStaysParkedForRPCReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t, ctx)
	h.setRows(statsRows()...)
	h.statsMove.Store(true)

	c := h.dial(ctx)
	if e := c.call("state.snapshot", rpc.StateSnapshotParams{Include: rpc.Include{"stats"}}, nil); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := h.sampleN.Load(); got != 0 {
		t.Errorf("the stats sampler ran %d times for an unsubscribed daemon", got)
	}

	var status rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &status); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	if status.ScanIntervalMs < int(scanner.BaseInterval/time.Millisecond) {
		t.Errorf("scan_interval_ms = %d, want at least the %s base", status.ScanIntervalMs, scanner.BaseInterval)
	}
}

// carriesStats reports whether a delta moved any port's stats.
func carriesStats(d state.Delta) bool {
	for _, p := range d.Ports.Updated {
		if p.Stats != nil {
			return true
		}
	}
	return false
}

// drain keeps a client's pipe empty. net.Pipe is unbuffered, so a subscriber
// nobody reads blocks the daemon's writer and starves every other one.
func drain(ctx context.Context, c *testClient) {
	for ctx.Err() == nil {
		c.conn.SetReadDeadline(time.Now().Add(pipeRead))
		if _, err := c.r.ReadBytes('\n'); err != nil {
			return
		}
	}
}

// The stats tick runs on its own clock, so it lands in the window between a
// bridge swapping its rows in and RemoteChanged announcing them. The remote
// host must be published exactly once, with a seq of its own, and the daemon's
// cached snapshot must agree with what went out.
func TestStatsTickBetweenARemoteChangeAndItsPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t, ctx)
	h.setRows(statsRows()...)
	h.statsMove.Store(true)

	src := &remoteSource{}
	h.loop.SetRemote(src.get)

	c := h.dial(ctx)
	c.subscribeAndSettle(rpc.StateSubscribeParams{
		Include: rpc.Include{"stats"}, Hosts: []string{"*"},
	})

	// The bridge writes, then at least one stats tick runs before the call
	// that is supposed to announce it.
	before := h.sampleN.Load()
	src.set(remoteRows("hetzner", 3000))
	deadline := time.Now().Add(5 * time.Second)
	for h.sampleN.Load() < before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.sampleN.Load() < before+2 {
		t.Fatal("no stats tick ran in the window; the test proved nothing")
	}
	h.loop.RemoteChanged()

	// Read a few seconds of deltas and count the ones announcing the host.
	announced, last := 0, uint64(0)
	for i := 0; i < 5; i++ {
		d := c.nextDelta()
		if d.Seq <= last {
			t.Fatalf("delta seq %d after %d: publish order is not seq order", d.Seq, last)
		}
		last = d.Seq
		for _, p := range d.Ports.Added {
			if p.Host == "hetzner" && p.Port == 3000 {
				announced++
			}
		}
	}
	if announced != 1 {
		t.Fatalf("the remote host was announced %d times, want exactly 1", announced)
	}

	// And a reader that arrives now sees what the stream said.
	var snap state.Snapshot
	if e := c.call("state.snapshot", map[string]any{"hosts": []string{"*"}}, &snap); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	found := false
	for _, p := range snap.Ports {
		if p.Host == "hetzner" && p.Port == 3000 {
			found = true
		}
	}
	if !found {
		t.Error("the snapshot lost the remote row the stream announced")
	}
}
