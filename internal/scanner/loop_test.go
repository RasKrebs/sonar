package scanner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// TestBackoffSequence pins the adaptive interval from the spec: base 2 s, ×1.5
// per unchanged tick, capped at 10 s when the daemon only answers RPC reads and
// at 5 s while someone is subscribed.
func TestBackoffSequence(t *testing.T) {
	tests := []struct {
		name string
		max  time.Duration
		want []time.Duration
	}{
		{
			name: "rpc reads only",
			max:  MaxInterval,
			want: []time.Duration{
				3 * time.Second,
				4500 * time.Millisecond,
				6750 * time.Millisecond,
				10 * time.Second,
				10 * time.Second,
			},
		},
		{
			name: "a subscriber is connected",
			max:  SubscribedMaxInterval,
			want: []time.Duration{
				3 * time.Second,
				4500 * time.Millisecond,
				5 * time.Second,
				5 * time.Second,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BaseInterval
			for i, w := range tt.want {
				got = backoff(got, BaseInterval, tt.max)
				if got != w {
					t.Fatalf("backoff step %d = %s, want %s", i+1, got, w)
				}
			}
		})
	}
}

func TestBackoffNeverDropsBelowBase(t *testing.T) {
	if got := backoff(time.Millisecond, BaseInterval, MaxInterval); got != BaseInterval {
		t.Errorf("backoff(1ms) = %s, want the base interval %s", got, BaseInterval)
	}
}

// TestLoopBacksOffThenSnapsBack drives the loop through unchanged scans and
// asserts the interval grows, then snaps back to the base as soon as the port
// set changes.
func TestLoopBacksOffThenSnapsBack(t *testing.T) {
	var mu sync.Mutex
	rows := []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 100, Process: "node"}}

	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]ports.ListeningPort{}, rows...), nil
		},
	})

	l.scanAndPublish(Include{})
	if got := l.Status().IntervalMs; got != int(BaseInterval/time.Millisecond) {
		t.Fatalf("after the first scan interval = %dms, want %d", got, BaseInterval/time.Millisecond)
	}

	l.scanAndPublish(Include{})
	l.scanAndPublish(Include{})
	if got, floor := l.Status().IntervalMs, int(BaseInterval/time.Millisecond); got <= floor {
		t.Fatalf("interval did not back off on unchanged scans: %dms", got)
	}

	mu.Lock()
	rows = append(rows, ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 200, Process: "python3"})
	mu.Unlock()

	l.scanAndPublish(Include{})
	if got, want := l.Status().IntervalMs, int(BaseInterval/time.Millisecond); got != want {
		t.Fatalf("interval after a change = %dms, want %d", got, want)
	}
}

// TestBackoffCapsAtMaxInterval: with nobody subscribed the daemon is serving
// RPC reads only and may back all the way off to 10 s.
func TestBackoffCapsAtMaxInterval(t *testing.T) {
	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 0, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
	})
	for i := 0; i < 20; i++ {
		l.scanAndPublish(Include{})
	}
	if got, want := l.Status().IntervalMs, int(MaxInterval/time.Millisecond); got != want {
		t.Errorf("interval = %dms, want the %dms cap", got, want)
	}
}

// TestBackoffCapsAtFiveSecondsWithASubscriber: a live view must never lag a
// change by more than the subscribed cap, so the backoff stops at 5 s while
// anyone is subscribed.
func TestBackoffCapsAtFiveSecondsWithASubscriber(t *testing.T) {
	subs := 0
	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return subs, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
	})

	subs = 1
	for i := 0; i < 20; i++ {
		l.scanAndPublish(Include{})
	}
	if got, want := l.Status().IntervalMs, int(SubscribedMaxInterval/time.Millisecond); got != want {
		t.Errorf("interval with a subscriber = %dms, want the %dms cap", got, want)
	}

	// The last subscriber leaves: the loop may back off the rest of the way.
	subs = 0
	for i := 0; i < 20; i++ {
		l.scanAndPublish(Include{})
	}
	if got, want := l.Status().IntervalMs, int(MaxInterval/time.Millisecond); got != want {
		t.Errorf("interval with no subscribers = %dms, want the %dms cap", got, want)
	}
}

// TestPublishesAddAndRemove is the delta contract the desktop app depends on:
// a new listener arrives in Ports.Added and a closed one in Ports.Removed.
func TestPublishesAddAndRemove(t *testing.T) {
	var mu sync.Mutex
	rows := []ports.ListeningPort{}

	var published []state.Delta
	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]ports.ListeningPort{}, rows...), nil
		},
		Publish: func(prev, next state.Snapshot, _ []state.Event) {
			published = append(published, state.Diff(prev, next))
		},
	})

	l.scanAndPublish(Include{})

	mu.Lock()
	rows = append(rows, ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	mu.Unlock()
	l.scanAndPublish(Include{})

	last := published[len(published)-1]
	if len(last.Ports.Added) != 1 || last.Ports.Added[0].Port != 8123 {
		t.Fatalf("expected 8123 in Added, got %+v", last.Ports)
	}

	mu.Lock()
	rows = rows[:0]
	mu.Unlock()
	l.scanAndPublish(Include{})

	last = published[len(published)-1]
	if len(last.Ports.Removed) != 1 || last.Ports.Removed[0] != "8123:127.0.0.1" {
		t.Fatalf("expected 8123:127.0.0.1 in Removed, got %+v", last.Ports)
	}
}

// TestSeqIsMonotonic checks that Seq only ever advances, and only when
// something changed.
func TestSeqIsMonotonic(t *testing.T) {
	var mu sync.Mutex
	n := 0
	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			mu.Lock()
			defer mu.Unlock()
			out := make([]ports.ListeningPort, n)
			for i := range out {
				out[i] = ports.ListeningPort{Port: 3000 + i, BindAddress: "127.0.0.1", PID: 1000 + i}
			}
			return out, nil
		},
	})

	seqs := []uint64{}
	for i := 0; i < 4; i++ {
		l.scanAndPublish(Include{})
		seqs = append(seqs, l.Status().Seq)
		mu.Lock()
		n++
		mu.Unlock()
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq did not advance: %v", seqs)
		}
	}

	before := l.Status().Seq
	l.scanAndPublish(Include{})
	l.scanAndPublish(Include{})
	if got := l.Status().Seq; got != before+1 {
		t.Fatalf("seq = %d after one change and one unchanged scan, want %d", got, before+1)
	}
}

// TestScanErrorKeepsLastGoodState is the spec's "never panics on a bad
// snapshot" rule: a failing scan emits scan_error and leaves state alone.
func TestScanErrorKeepsLastGoodState(t *testing.T) {
	var fail atomic.Bool
	var events []state.Event

	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			if fail.Load() {
				return nil, errors.New("lsof: not found")
			}
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 7}}, nil
		},
		Publish: func(_, _ state.Snapshot, ev []state.Event) {
			events = append(events, ev...)
		},
	})

	l.scanAndPublish(Include{})
	good := l.Cached()

	fail.Store(true)
	l.scanAndPublish(Include{})

	if got := l.Cached(); got.Seq != good.Seq || len(got.Ports) != 1 {
		t.Fatalf("failed scan clobbered the last good snapshot: %+v", got)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == "scan_error" {
			found = true
		}
	}
	if !found {
		t.Errorf("no scan_error event emitted; got %+v", events)
	}
}

// TestSnapshotUsesTheCacheWithinTTL checks the 2 s result cache RPC reads serve
// from.
func TestSnapshotUsesTheCacheWithinTTL(t *testing.T) {
	var scans atomic.Int64
	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return 0, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 7}}, nil
		},
	})

	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if got := scans.Load(); got != 1 {
		t.Errorf("two reads inside the %s cache ran %d scans, want 1", CacheTTL, got)
	}
}

// TestLoopParksWithNoSubscribers is the "stops entirely with zero subscribers"
// rule: no subscribers, no scans, until something wakes it.
func TestLoopParksWithNoSubscribers(t *testing.T) {
	var scans atomic.Int64
	var subs atomic.Int64

	l := New(Options{
		HostStats: fixedHost,
		Demand:    func() (int, Include) { return int(subs.Load()), Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans.Add(1)
			return nil, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	time.Sleep(150 * time.Millisecond)
	if got := scans.Load(); got != 0 {
		t.Fatalf("loop scanned %d times with no subscribers, want 0", got)
	}

	subs.Store(1)
	l.Wake()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && scans.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if scans.Load() == 0 {
		t.Fatal("loop did not scan after a subscriber arrived")
	}
}

// fixedHost is a host collector that answers the same row every time, so a
// test's seq and deltas move only when the ports it fakes do. The production
// collector reports live cpu, which changes on nearly every tick.
func fixedHost(context.Context) (state.Host, error) {
	return state.Host{Kernel: "test-kernel", OS: "testos", Arch: "testarch"}, nil
}

// TestConfiguredScanIntervalMovesTheWholeCurve is `daemon.scan_interval`: the
// value sets the base, and the two ceilings scale with it, so a daemon told to
// scan every 5 s backs off to 12.5 s with a subscriber and 25 s without one
// rather than to the constants that belong to the 2 s default.
func TestConfiguredScanIntervalMovesTheWholeCurve(t *testing.T) {
	subs := 1
	l := New(Options{
		HostStats:    fixedHost,
		ScanInterval: 5 * time.Second,
		Demand:       func() (int, Include) { return subs, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
	})

	l.scanAndPublish(Include{})
	if got, want := l.Status().IntervalMs, 5000; got != want {
		t.Fatalf("after the first scan interval = %dms, want the configured base %dms", got, want)
	}

	for i := 0; i < 20; i++ {
		l.scanAndPublish(Include{})
	}
	if got, want := l.Status().IntervalMs, 12500; got != want {
		t.Errorf("subscribed cap = %dms, want %dms (%v x the base)", got, want, SubscribedMaxFactor)
	}

	subs = 0
	for i := 0; i < 20; i++ {
		l.scanAndPublish(Include{})
	}
	if got, want := l.Status().IntervalMs, 25000; got != want {
		t.Errorf("idle cap = %dms, want %dms (%d x the base)", got, want, IdleMaxFactor)
	}
}

// TestConfiguredScanIntervalPacesTheTick: the interval the loop waits out
// between ticks is the configured base, not the 2 s constant. The clock is
// injected so the assertion is about the cadence rather than about how long
// the test was willing to sleep.
func TestConfiguredScanIntervalPacesTheTick(t *testing.T) {
	frozen := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	now := frozen
	l := New(Options{
		HostStats:    fixedHost,
		ScanInterval: 6 * time.Second,
		Now:          func() time.Time { return now },
		Demand:       func() (int, Include) { return 1, Include{} },
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 3000, BindAddress: "127.0.0.1", PID: 1}}, nil
		},
	})

	l.scanAndPublish(Include{})
	if got := l.dueIn(); got != 6*time.Second {
		t.Fatalf("dueIn right after a scan = %s, want the configured base 6s", got)
	}
	now = frozen.Add(5 * time.Second)
	if got := l.dueIn(); got != time.Second {
		t.Errorf("dueIn 5s into a 6s base = %s, want 1s", got)
	}
	now = frozen.Add(6 * time.Second)
	if got := l.dueIn(); got > 0 {
		t.Errorf("dueIn once the base has elapsed = %s, want a scan to be due", got)
	}
}

// A scan_interval below the floor is clamped rather than honoured: a daemon
// asked to scan every 10ms would spend the machine on `lsof`.
func TestScanIntervalIsClampedToTheFloor(t *testing.T) {
	l := New(Options{HostStats: fixedHost, ScanInterval: 10 * time.Millisecond})
	if got, want := l.Status().BaseIntervalMs, int(MinScanInterval/time.Millisecond); got != want {
		t.Errorf("base = %dms, want the %dms floor", got, want)
	}
}

// TestStatusReportsTheEffectiveIntervals: both cadences are read once at
// startup, so `daemon.status` is the only way to tell what a running daemon
// actually settled on.
func TestStatusReportsTheEffectiveIntervals(t *testing.T) {
	l := New(Options{
		HostStats:     fixedHost,
		ScanInterval:  3 * time.Second,
		StatsInterval: 500 * time.Millisecond,
	})
	st := l.Status()
	if st.BaseIntervalMs != 3000 {
		t.Errorf("BaseIntervalMs = %d, want 3000", st.BaseIntervalMs)
	}
	if st.StatsIntervalMs != 500 {
		t.Errorf("StatsIntervalMs = %d, want 500", st.StatsIntervalMs)
	}

	def := New(Options{HostStats: fixedHost}).Status()
	if def.BaseIntervalMs != int(BaseInterval/time.Millisecond) {
		t.Errorf("default BaseIntervalMs = %d, want %d", def.BaseIntervalMs, BaseInterval/time.Millisecond)
	}
	if def.StatsIntervalMs != int(StatsInterval/time.Millisecond) {
		t.Errorf("default StatsIntervalMs = %d, want %d", def.StatsIntervalMs, StatsInterval/time.Millisecond)
	}
}
