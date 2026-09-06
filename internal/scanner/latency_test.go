package scanner

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// Step 1A.19. Everything here is about what a handler pays for, not about what
// it publishes: the shape of the answers is pinned by the 1A.15, 1A.7 and
// 1A.16 tests next to these.

// TestRepublishDoesNotScan is the step's headline. A store write's republish
// used to run a whole scan — `lsof`, `ps`, `docker stats`, a round of health
// probes — before its own reply, which is what made "Save color" in the app
// take double figures of seconds.
func TestRepublishDoesNotScan(t *testing.T) {
	st := openStore(t)
	var scans atomic.Int64
	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})

	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans after the first read = %d, want 1", got)
	}

	if err := st.SetRename("port:8123", "storefront"); err != nil {
		t.Fatal(err)
	}
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans after Republish = %d, want no new scan", got)
	}

	snap := l.Cached()
	if len(snap.Ports) != 1 || snap.Ports[0].DisplayName != "storefront" {
		t.Fatalf("cached snapshot = %+v, want the rename published from it", snap.Ports)
	}
}

// TestRepublishPublishesBeforeItReturns keeps contract §18's ordering: the
// delta carrying the change is on the wire before the handler's reply, which
// is the reason republish is synchronous at all.
func TestRepublishPublishesBeforeItReturns(t *testing.T) {
	st := openStore(t)
	var mu sync.Mutex
	var names []string
	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Store:         st,
		Demand:        func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
		Publish: func(_, next state.Snapshot, _ []state.Event) {
			mu.Lock()
			defer mu.Unlock()
			for _, p := range next.Ports {
				names = append(names, p.DisplayName)
			}
		},
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := st.SetRename("port:8123", "storefront"); err != nil {
		t.Fatal(err)
	}
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(names) == 0 || names[len(names)-1] != "storefront" {
		t.Fatalf("published names = %v, want the rename published by the time Republish returned", names)
	}
}

// TestRepublishBeforeAnyScanFallsBackToOne: with nothing scanned yet there is
// nothing to re-attribute, and the write still has to reach the wire.
func TestRepublishBeforeAnyScanFallsBackToOne(t *testing.T) {
	var scans atomic.Int64
	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})
	if err := l.Republish(); err != nil {
		t.Fatalf("Republish: %v", err)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scans = %d, want one scan when there is no snapshot to re-attribute", got)
	}
}

// TestRPCScanDoesNotInheritDemandButKeepsIt is the other half of the fix. A
// scan an RPC starts collects what the *caller* asked for; what the
// subscribers asked for is copied forward from the last snapshot instead of
// being collected again, so `ports.kill` never waits on `docker stats` or a
// health probe (contract §44) and no subscriber sees stats blink away.
func TestRPCScanDoesNotInheritDemandButKeepsIt(t *testing.T) {
	var mu sync.Mutex
	var asked []Include

	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Demand:        func() (int, Include) { return 1, Include{Stats: true, Health: true} },
		Scan: func(inc Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			asked = append(asked, inc)
			mu.Unlock()
			row := ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"}
			if inc.Stats {
				row.CPUPercent = 12.5
				row.MemoryRSS = 4096
			}
			if inc.Health {
				row.HealthStatus = "healthy"
			}
			return []ports.ListeningPort{row}, nil
		},
	})

	// The loop's own tick collects the demand.
	l.scanAndPublish(Include{Stats: true, Health: true})
	ticked := l.Cached()
	if len(ticked.Ports) != 1 || ticked.Ports[0].Health == nil {
		t.Fatalf("the tick published %+v, want a probed row", ticked.Ports)
	}
	want := *ticked.Ports[0].Health

	// A kill's rescan asks for nothing.
	snap, err := l.Rescan(Include{})
	if err != nil {
		t.Fatalf("Rescan: %v", err)
	}

	mu.Lock()
	got := append([]Include{}, asked...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("scans = %d, want the tick and the rescan", len(got))
	}
	if got[1].Stats || got[1].Health {
		t.Fatalf("the rescan collected %+v, want a bare scan", got[1])
	}
	if len(snap.Ports) != 1 {
		t.Fatalf("ports = %+v", snap.Ports)
	}
	if snap.Ports[0].Stats == nil || snap.Ports[0].Stats.CPUPercent != 12.5 {
		t.Fatalf("stats = %+v, want the subscriber's last reading carried forward", snap.Ports[0].Stats)
	}
	if snap.Ports[0].Health == nil || *snap.Ports[0].Health != want {
		t.Fatalf("health = %+v, want the tick's verdict %+v carried forward", snap.Ports[0].Health, want)
	}
}

// TestOlderScanNeverOvertakesANewerOne is what lets an RPC's bare scan run
// alongside the loop's expensive one: overlapping is fine, going backwards is
// not. A scan whose OS half began before the published snapshot's must not
// commit (contract §44).
func TestOlderScanNeverOvertakesANewerOne(t *testing.T) {
	release := make(chan struct{})
	var slow atomic.Bool

	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Scan: func(inc Include) ([]ports.ListeningPort, error) {
			if inc.Stats {
				// The loop's tick: started first, finishes last, and still
				// sees the port that the bare scan below saw go away.
				slow.Store(true)
				<-release
				return []ports.ListeningPort{
					{Port: 8123, PID: 42, Process: "python3"},
					{Port: 8124, PID: 43, Process: "python3"},
				}, nil
			}
			return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
		},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		l.scanAndPublish(Include{Stats: true})
	}()
	for !slow.Load() {
		time.Sleep(time.Millisecond)
	}

	// The bare scan starts second and commits first: one port.
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatalf("Rescan: %v", err)
	}
	if got := len(l.Cached().Ports); got != 1 {
		t.Fatalf("ports after the rescan = %d, want 1", got)
	}

	close(release)
	<-done

	if got := len(l.Cached().Ports); got != 1 {
		t.Fatalf("ports after the older scan committed = %d, want the newer scan to still win", got)
	}
}

// TestLockForGivesUp pins the deadline every handler path takes a gate with. A
// scan that never returns must not turn into a reply that never comes.
func TestLockForGivesUp(t *testing.T) {
	gate := make(chan struct{}, 1)
	lock(gate)
	start := time.Now()
	if lockFor(gate, 20*time.Millisecond) {
		t.Fatal("lockFor took a gate that was held")
	}
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("gave up after %s, want at least the budget", waited)
	}
	unlock(gate)
	if !lockFor(gate, time.Second) {
		t.Fatal("lockFor could not take a free gate")
	}
	unlock(gate)
}

// TestConfiguredHealthRoundIsBudgeted: a service that accepts and never
// answers must not add waves of HealthTimeout to the scan every write is
// queued behind.
func TestConfiguredHealthRoundIsBudgeted(t *testing.T) {
	rows := make([]state.Port, 0, 40)
	gg := []state.Group{{Name: "slow"}}
	for i := 0; i < 40; i++ {
		port := 9000 + i
		rows = append(rows, state.Port{Port: port, BindAddress: "127.0.0.1"})
		path := "/healthz"
		p := port
		gg[0].Services = append(gg[0].Services, state.Service{
			Name: "svc", Health: &path, PortActual: &p,
		})
	}

	start := time.Now()
	probeConfigured(rows, gg, func(string, int, string, time.Duration) ports.HealthResult {
		time.Sleep(50 * time.Millisecond)
		return ports.HealthResult{Status: "timeout"}
	}, 80*time.Millisecond)
	took := time.Since(start)

	if took > 400*time.Millisecond {
		t.Fatalf("a round of 40 probes took %s, want it bounded by the budget", took)
	}
	if rows[0].Health == nil {
		t.Fatal("the first probe should still have run and landed")
	}
}
