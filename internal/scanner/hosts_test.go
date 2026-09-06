package scanner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// movingHost is a collector whose cpu percent climbs on every call, the way a
// real machine's does.
func movingHost() func(context.Context) (state.Host, error) {
	var mu sync.Mutex
	pct := 0.0
	return func(context.Context) (state.Host, error) {
		mu.Lock()
		defer mu.Unlock()
		pct += 1.5
		p := pct
		return state.Host{Kernel: "test-kernel", CPUPercent: &p}, nil
	}
}

// Every snapshot names this machine, with the collection's counts and the
// daemon's own versions filled in by the loop.
func TestScanPublishesLocalhost(t *testing.T) {
	l := New(Options{
		DaemonVersion:   "9.9.9",
		ProtocolVersion: "1.0.0",
		HostStats:       fixedHost,
		Demand:          func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{
				{Port: 3000, BindAddress: "127.0.0.1", PID: 1},
				{Port: 5173, BindAddress: "127.0.0.1", PID: 2},
			}, nil
		},
	})
	l.scanAndPublish(Include{})

	snap := l.Cached()
	if len(snap.Hosts) != 1 {
		t.Fatalf("hosts = %+v, want one row", snap.Hosts)
	}
	h := snap.Hosts[0]
	if h.Name != state.LocalhostName || h.Address != state.LocalhostName {
		t.Errorf("host identity = %q/%q, want localhost/localhost", h.Name, h.Address)
	}
	if h.Status != state.HostConnected || h.LatencyMs != 0 {
		t.Errorf("localhost status = %q latency %d, want connected / 0", h.Status, h.LatencyMs)
	}
	if h.DaemonVersion != "9.9.9" || h.ProtocolVersion != "1.0.0" {
		t.Errorf("versions = %q/%q, want the daemon's own", h.DaemonVersion, h.ProtocolVersion)
	}
	if h.Ports != 2 {
		t.Errorf("ports = %d, want the 2 in the snapshot", h.Ports)
	}
	if h.Kernel != "test-kernel" {
		t.Errorf("kernel = %q, want the collector's answer", h.Kernel)
	}
	if h.LastSeen != snap.At {
		t.Errorf("last_seen = %q, want the scan time %q", h.LastSeen, snap.At)
	}
}

// A client that subscribes before the first scan still finds localhost: the
// row is identity even when there is no load to report yet.
func TestCachedSnapshotBeforeTheFirstScanNamesLocalhost(t *testing.T) {
	l := New(Options{DaemonVersion: "9.9.9", ProtocolVersion: "1.0.0", HostStats: fixedHost})
	snap := l.Cached()
	if len(snap.Hosts) != 1 || snap.Hosts[0].Name != state.LocalhostName {
		t.Fatalf("hosts before the first scan = %+v, want localhost", snap.Hosts)
	}
	if snap.Hosts[0].CPUPercent != nil {
		t.Error("cpu_percent must be null before anything has been measured")
	}
}

// A collector that fails is not a scan failure: the row is still published,
// with the reason on it and the load null.
func TestHostStatsFailureStillPublishesLocalhost(t *testing.T) {
	l := New(Options{
		HostStats: func(context.Context) (state.Host, error) {
			return state.Host{}, errors.New("/proc is not mounted")
		},
		Demand: func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
	})
	l.scanAndPublish(Include{})

	hosts := l.Cached().Hosts
	if len(hosts) != 1 {
		t.Fatalf("hosts = %+v, want the localhost row anyway", hosts)
	}
	if hosts[0].Status != state.HostConnected {
		t.Errorf("status = %q, want connected: the daemon is talking to itself", hosts[0].Status)
	}
	if hosts[0].StatusReason == nil || *hosts[0].StatusReason != "/proc is not mounted" {
		t.Errorf("status_reason = %v, want the collector's error", hosts[0].StatusReason)
	}
	if hosts[0].CPUPercent != nil {
		t.Error("cpu_percent must be null when the collection failed")
	}
}

// Host load moves on nearly every tick. It is published — one `hosts` delta
// per tick, to every subscriber whatever their `include` — but it must not
// snap the scan interval back to the base, or a subscribed daemon would run a
// full port scan every two seconds forever.
func TestHostOnlyChangePublishesWithoutResettingTheBackoff(t *testing.T) {
	var published []state.Delta
	l := New(Options{
		HostStats: movingHost(),
		Demand:    func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
		Publish: func(prev, next state.Snapshot, _ []state.Event) {
			published = append(published, state.Diff(prev, next))
		},
	})

	for i := 0; i < 5; i++ {
		l.scanAndPublish(Include{})
	}

	if got, want := l.Status().IntervalMs, int(SubscribedMaxInterval/time.Millisecond); got != want {
		t.Errorf("interval = %dms after five host-only ticks, want the %dms cap", got, want)
	}
	if len(published) != 5 {
		t.Fatalf("published %d deltas, want one per tick", len(published))
	}
	last := published[len(published)-1]
	if len(last.Hosts.Updated) != 1 {
		t.Fatalf("the last delta carried %+v, want one host update", last.Hosts)
	}
	if len(last.Ports.Added)+len(last.Ports.Updated)+len(last.Ports.Removed) != 0 {
		t.Errorf("a host-only tick published port changes: %+v", last.Ports)
	}
	if l.Status().Seq != 5 {
		t.Errorf("seq = %d, want one per published tick", l.Status().Seq)
	}
}

// A tick where nothing moved at all — ports the same, machine the same —
// publishes nothing and keeps its sequence number.
func TestFullyUnchangedTickPublishesNothing(t *testing.T) {
	published := 0
	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
		Publish: func(state.Snapshot, state.Snapshot, []state.Event) { published++ },
	})
	l.scanAndPublish(Include{})
	seq := l.Status().Seq
	l.scanAndPublish(Include{})
	l.scanAndPublish(Include{})
	if published != 1 {
		t.Errorf("published %d times, want only the first tick", published)
	}
	if l.Status().Seq != seq {
		t.Errorf("seq moved to %d on an unchanged tick, want %d", l.Status().Seq, seq)
	}
}
