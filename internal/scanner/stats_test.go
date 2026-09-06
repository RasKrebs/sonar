package scanner

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// statsRig is a loop whose scan, host collector and per-process sampler are all
// fakes, wired for the stats-only tick: the scan reports a fixed port set so
// nothing but stats can move, and the sampler's cpu figure climbs on every
// call the way a real one does.
type statsRig struct {
	loop *Loop

	mu       sync.Mutex
	rows     []ports.ListeningPort
	gonePIDs map[int]bool
	deltas   []published

	scans    atomic.Int64
	samples  atomic.Int64
	hostCPU  atomic.Int64 // tenths of a percent, so the host row moves too
	sampleAt atomic.Int64
	subs     atomic.Int64
	include  atomic.Bool
}

// published is one (prev, next) transition the loop handed to its publisher.
type published struct {
	prev, next state.Snapshot
	at         time.Time
}

func newStatsRig(t *testing.T) *statsRig {
	t.Helper()
	r := &statsRig{
		gonePIDs: map[int]bool{},
		rows: []ports.ListeningPort{
			{Port: 3000, BindAddress: "127.0.0.1", PID: 100, Process: "node", CPUPercent: 1, MemoryRSS: 1 << 20, Connections: 7, ThreadCount: 4},
			{Port: 5173, BindAddress: "127.0.0.1", PID: 200, Process: "vite", CPUPercent: 2, MemoryRSS: 2 << 20, Connections: 3, ThreadCount: 9},
		},
	}
	r.loop = New(Options{
		DaemonVersion: "test",
		StatsInterval: 30 * time.Millisecond,
		Demand: func() (int, Include) {
			return int(r.subs.Load()), Include{Stats: r.include.Load()}
		},
		Publish: func(prev, next state.Snapshot, _ []state.Event) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.deltas = append(r.deltas, published{prev: prev, next: next, at: time.Now()})
		},
		Scan: func(Include) ([]ports.ListeningPort, error) {
			r.scans.Add(1)
			r.mu.Lock()
			defer r.mu.Unlock()
			return append([]ports.ListeningPort{}, r.rows...), nil
		},
		SampleStats: func(pids []int) map[int]ports.ProcSample {
			n := r.samples.Add(1)
			r.sampleAt.Store(time.Now().UnixNano())
			r.mu.Lock()
			defer r.mu.Unlock()
			out := map[int]ports.ProcSample{}
			for _, pid := range pids {
				if r.gonePIDs[pid] {
					continue
				}
				out[pid] = ports.ProcSample{
					CPUPercent:  float64(n),
					MemoryRSS:   int64(pid) << 10,
					ThreadCount: 11,
					State:       "running",
					Uptime:      "1s",
				}
			}
			return out
		},
		HostStats: func(context.Context) (state.Host, error) {
			pct := float64(r.hostCPU.Add(5)) / 10
			return state.Host{Kernel: "test-kernel", CPUPercent: &pct}, nil
		},
	})
	return r
}

// run starts the loop and stops it when the test ends.
func (r *statsRig) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.loop.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// subscribe fakes a client connecting with the given include set.
func (r *statsRig) subscribe(stats bool) {
	r.include.Store(stats)
	r.subs.Store(1)
	r.loop.Wake()
}

func (r *statsRig) unsubscribe() {
	r.subs.Store(0)
	r.include.Store(false)
	r.loop.Wake()
}

func (r *statsRig) published() []published {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]published{}, r.deltas...)
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The sampler is dead weight on a daemon nobody is watching: `sonar list` and
// `daemon status` go through Snapshot, which wakes the loop, and that must not
// start a 1 s `ps` on a machine with no subscriber.
func TestStatsTickParksUntilSomeoneSubscribes(t *testing.T) {
	r := newStatsRig(t)
	r.run(t)

	// An RPC read: it scans and wakes, but nobody is subscribed.
	if _, err := r.loop.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := r.samples.Load(); got != 0 {
		t.Fatalf("the stats sampler ran %d times with no subscribers, want 0", got)
	}

	r.subscribe(true)
	waitFor(t, "the sampler to start on the first subscribe", func() bool {
		return r.samples.Load() > 0
	})

	r.unsubscribe()
	// Let the tick in flight finish, then check it stops for good.
	time.Sleep(100 * time.Millisecond)
	before := r.samples.Load()
	time.Sleep(250 * time.Millisecond)
	if after := r.samples.Load(); after != before {
		t.Errorf("the sampler ran %d more times after the last unsubscribe", after-before)
	}
}

// The tick keeps its own cadence: several samples land inside one base scan
// interval, and the scan is not what produced them.
func TestStatsTickRunsOnItsOwnCadence(t *testing.T) {
	r := newStatsRig(t)
	r.run(t)
	r.subscribe(true)

	waitFor(t, "four stats samples", func() bool { return r.samples.Load() >= 4 })
	if scans := r.scans.Load(); scans > 3 {
		t.Errorf("%d port scans while four stats samples were taken; the scan cadence was dragged along", scans)
	}
}

// A stats tick publishes a delta whose only moving parts are `stats` and the
// host row. Everything that identifies a port — display name, group, health,
// started_at, connections — is carried through untouched, which is what makes
// the delta invisible to a subscriber that did not ask for stats.
func TestStatsTickMovesOnlyStatsAndHost(t *testing.T) {
	r := newStatsRig(t)
	r.loop.scanAndPublish(Include{Stats: true})

	before := r.loop.Cached()
	r.include.Store(true)
	r.loop.sampleStats(Include{Stats: true})
	after := r.loop.Cached()

	if after.Seq != before.Seq+1 {
		t.Fatalf("seq = %d, want %d: a published stats tick takes the next seq", after.Seq, before.Seq+1)
	}
	if len(after.Ports) != len(before.Ports) {
		t.Fatalf("port count changed: %d -> %d", len(before.Ports), len(after.Ports))
	}
	for i := range after.Ports {
		a, b := before.Ports[i], after.Ports[i]
		if a.Stats == nil || b.Stats == nil {
			t.Fatalf("port %s lost its stats object", b.Key())
		}
		if *a.Stats == *b.Stats {
			t.Errorf("port %s stats did not move: %+v", b.Key(), *b.Stats)
		}
		if b.Stats.Connections != a.Stats.Connections {
			t.Errorf("connections moved on a stats tick (%d -> %d); counting them is a per-port lsof and belongs on the scan",
				a.Stats.Connections, b.Stats.Connections)
		}
		a.Stats, b.Stats = nil, nil
		if !reflect.DeepEqual(a, b) {
			t.Errorf("a stats tick changed a non-stats field:\n before %+v\n after  %+v", a, b)
		}
	}

	if len(after.Hosts) != 1 || after.Hosts[0].CPUPercent == nil {
		t.Fatalf("hosts = %+v, want the localhost row with a load", after.Hosts)
	}
	if *after.Hosts[0].CPUPercent == *before.Hosts[0].CPUPercent {
		t.Error("the host load did not move on a stats tick")
	}
	if after.Hosts[0].Ports != len(after.Ports) {
		t.Errorf("host.ports = %d, want the %d rows in the snapshot", after.Hosts[0].Ports, len(after.Ports))
	}
}

// The whole point of the split: a stats tick must never reset the scan
// backoff, or a subscribed daemon is pinned to a full port scan every second
// (contract §37's rule for host-only changes, now enforced for stats too).
func TestStatsTickNeverResetsTheScanInterval(t *testing.T) {
	r := newStatsRig(t)

	// Back the scan interval off with unchanged scans.
	for i := 0; i < 4; i++ {
		r.loop.scanAndPublish(Include{Stats: true})
	}
	backedOff := r.loop.Status().IntervalMs
	if backedOff <= int(BaseInterval/time.Millisecond) {
		t.Fatalf("interval = %dms, expected the backoff to have grown past %s", backedOff, BaseInterval)
	}
	scans := r.scans.Load()

	for i := 0; i < 5; i++ {
		r.loop.sampleStats(Include{Stats: true})
	}

	st := r.loop.Status()
	if st.IntervalMs != backedOff {
		t.Errorf("the stats tick moved the scan interval: %dms -> %dms", backedOff, st.IntervalMs)
	}
	if st.Scans != scans {
		t.Errorf("scan count = %d, want %d: a stats tick is not a scan", st.Scans, scans)
	}
	if r.scans.Load() != scans {
		t.Errorf("the stats tick called the OS scan %d times", r.scans.Load()-scans)
	}
}

// Nothing moved, nothing published: an empty delta is never sent (contract
// §15), and a snapshot nobody was told about does not consume a seq.
func TestStatsTickSuppressesAnEmptyDelta(t *testing.T) {
	r := newStatsRig(t)
	// A sampler and a host collector that both answer the same thing forever.
	fixed := map[int]ports.ProcSample{
		100: {CPUPercent: 1, MemoryRSS: 1 << 20, ThreadCount: 4, State: "running", Uptime: "1s"},
		200: {CPUPercent: 2, MemoryRSS: 2 << 20, ThreadCount: 9, State: "running", Uptime: "1s"},
	}
	r.loop.opts.SampleStats = func([]int) map[int]ports.ProcSample { return fixed }
	r.loop.opts.HostStats = fixedHost

	r.loop.scanAndPublish(Include{Stats: true})
	// One tick to settle: the scan's own stats and the sampler's disagree
	// about uptime and state, so the first sample after a scan really does
	// move something.
	r.loop.sampleStats(Include{Stats: true})
	seq := r.loop.Cached().Seq
	published := len(r.published())

	for i := 0; i < 3; i++ {
		r.loop.sampleStats(Include{Stats: true})
	}

	if got := len(r.published()); got != published {
		t.Errorf("%d deltas published by stats ticks where nothing moved", got-published)
	}
	if got := r.loop.Cached().Seq; got != seq {
		t.Errorf("seq = %d, want %d: an unpublished tick must not burn a sequence number", got, seq)
	}
}

// A process that exits between the scan that listed it and the sample that
// refreshes it is absent from the sample. Its row keeps the stats it had —
// zeroes would read like facts — and the next port scan is what removes it.
func TestStatsTickHandlesAPIDDisappearing(t *testing.T) {
	r := newStatsRig(t)
	r.loop.scanAndPublish(Include{Stats: true})
	r.loop.sampleStats(Include{Stats: true})
	before := r.loop.Cached()

	r.mu.Lock()
	r.gonePIDs[100] = true
	r.mu.Unlock()

	r.loop.sampleStats(Include{Stats: true})
	after := r.loop.Cached()

	if len(after.Ports) != len(before.Ports) {
		t.Fatalf("the stats tick removed a row: %d -> %d ports", len(before.Ports), len(after.Ports))
	}
	gone, alive := after.Ports[0], after.Ports[1]
	if gone.PID != 100 || alive.PID != 200 {
		t.Fatalf("unexpected row order: %+v", after.Ports)
	}
	if *gone.Stats != *before.Ports[0].Stats {
		t.Errorf("a vanished pid's stats were rewritten: %+v -> %+v", *before.Ports[0].Stats, *gone.Stats)
	}
	if *alive.Stats == *before.Ports[1].Stats {
		t.Error("the surviving pid's stats were not refreshed")
	}
}

// A subscriber that asked for neither stats nor health still gets the host
// load, and the sampler does not fork a `ps` for per-process stats nobody
// wants.
func TestStatsTickWithoutStatsIncludeOnlyMovesTheHost(t *testing.T) {
	r := newStatsRig(t)
	r.loop.scanAndPublish(Include{})
	before := r.loop.Cached()

	r.loop.sampleStats(Include{})
	after := r.loop.Cached()

	if got := r.samples.Load(); got != 0 {
		t.Errorf("the per-process sampler ran %d times for a subscriber that did not ask for stats", got)
	}
	if after.Seq != before.Seq+1 {
		t.Errorf("seq = %d, want %d: the host load still moved", after.Seq, before.Seq+1)
	}
	if *after.Hosts[0].CPUPercent == *before.Hosts[0].CPUPercent {
		t.Error("the host load did not move")
	}
}

// Stats ticks and scans are serialized end to end, so `seq` order is publish
// order (contract §38). Run under -race: this is the test that catches a
// sampler committing a snapshot in the middle of a scan's commit.
func TestStatsTickIsSerializedWithScans(t *testing.T) {
	r := newStatsRig(t)
	r.run(t)
	r.subscribe(true)

	// Writers hammering Rescan the way ports.rename does, while the stats
	// tick runs at 30 ms.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := r.loop.Rescan(Include{Stats: true}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	waitFor(t, "a few stats samples under load", func() bool { return r.samples.Load() >= 5 })
	close(stop)
	wg.Wait()

	seen := r.published()
	if len(seen) < 2 {
		t.Fatalf("only %d publishes; the test did not exercise anything", len(seen))
	}
	var last uint64
	for i, p := range seen {
		if p.next.Seq <= last {
			t.Fatalf("publish %d has seq %d after seq %d: publish order is not seq order", i, p.next.Seq, last)
		}
		last = p.next.Seq
	}
}

// A stats tick refreshes this machine only. A registered remote host's rows —
// its ports and its `hosts` entry — are merged back in untouched, and its pids
// are never handed to the local process table: pid 100 on a build server is
// not pid 100 here.
func TestStatsTickLeavesRemoteRowsAlone(t *testing.T) {
	r := newStatsRig(t)

	var sampled []int
	inner := r.loop.opts.SampleStats
	r.loop.opts.SampleStats = func(pids []int) map[int]ports.ProcSample {
		sampled = append([]int{}, pids...)
		return inner(pids)
	}

	remotePort := state.Port{
		Port: 3000, BindAddress: "0.0.0.0", PID: 100, Process: "node", Host: "build",
		Stats: &state.Stats{CPUPercent: 42, MemoryRSS: 99, ThreadCount: 2},
	}
	r.loop.opts.Remote = func() state.Rows {
		return state.Rows{
			Ports: []state.Port{remotePort},
			Hosts: []state.Host{{Name: "build", Address: "build", Status: state.HostConnected}},
		}
	}

	r.loop.scanAndPublish(Include{Stats: true})
	r.loop.sampleStats(Include{Stats: true})

	for _, pid := range sampled {
		if pid != 100 && pid != 200 {
			t.Fatalf("the sampler was handed pid %d, which is not one of this machine's", pid)
		}
	}
	if len(sampled) != 2 {
		t.Fatalf("sampled pids = %v, want this machine's two (a remote pid 100 must not be added)", sampled)
	}

	snap := r.loop.CachedAll()
	var found *state.Port
	for i := range snap.Ports {
		if !state.IsLocalhost(snap.Ports[i].Host) {
			found = &snap.Ports[i]
		}
	}
	if found == nil {
		t.Fatal("the stats tick dropped the remote host's port row")
	}
	if found.Stats == nil || *found.Stats != *remotePort.Stats {
		t.Errorf("remote stats = %+v, want the bridge's own %+v", found.Stats, remotePort.Stats)
	}
	names := map[string]bool{}
	for _, h := range snap.Hosts {
		names[h.Name] = true
	}
	if !names["build"] || !names[state.LocalhostName] {
		t.Errorf("hosts after a stats tick = %v, want both localhost and the remote", names)
	}
}
