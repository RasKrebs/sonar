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
// per unchanged tick, capped at 10 s.
func TestBackoffSequence(t *testing.T) {
	want := []time.Duration{
		3 * time.Second,
		4500 * time.Millisecond,
		6750 * time.Millisecond,
		10 * time.Second,
		10 * time.Second,
	}
	got := BaseInterval
	for i, w := range want {
		got = backoff(got)
		if got != w {
			t.Fatalf("backoff step %d = %s, want %s", i+1, got, w)
		}
	}
}

func TestBackoffNeverDropsBelowBase(t *testing.T) {
	if got := backoff(time.Millisecond); got != BaseInterval {
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
		Demand: func() (int, Include) { return 1, Include{} },
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

func TestBackoffCapsAtMaxInterval(t *testing.T) {
	l := New(Options{
		Demand: func() (int, Include) { return 1, Include{} },
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

// TestPublishesAddAndRemove is the delta contract the desktop app depends on:
// a new listener arrives in Ports.Added and a closed one in Ports.Removed.
func TestPublishesAddAndRemove(t *testing.T) {
	var mu sync.Mutex
	rows := []ports.ListeningPort{}

	var published []state.Delta
	l := New(Options{
		Demand: func() (int, Include) { return 1, Include{} },
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
		Demand: func() (int, Include) { return 1, Include{} },
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
		Demand: func() (int, Include) { return 1, Include{} },
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
		Demand: func() (int, Include) { return 0, Include{} },
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
		Demand: func() (int, Include) { return int(subs.Load()), Include{} },
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
