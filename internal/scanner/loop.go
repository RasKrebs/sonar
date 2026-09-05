// Package scanner owns the daemon's single scan goroutine. It wraps the
// existing ports/docker enrichment pipeline, keeps the last good Snapshot, and
// publishes (previous, next) pairs so the daemon can compute whichever delta
// flavours its subscribers asked for.
package scanner

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// Timing constants from the spec's "Scanner loop" section.
const (
	// BaseInterval is the interval while anything is changing.
	BaseInterval = 2 * time.Second
	// MaxInterval caps the backoff on unchanged scans.
	MaxInterval = 10 * time.Second
	// BackoffFactor multiplies the interval after an unchanged scan.
	BackoffFactor = 1.5
	// CacheTTL is how long an RPC read may reuse the last scan.
	CacheTTL = 2 * time.Second
	// HealthCadence keeps HTTP probes off the port-scan cadence.
	HealthCadence = 10 * time.Second
	// HealthTimeout bounds a single probe.
	HealthTimeout = 2 * time.Second
)

// Include is the per-subscriber opt-in from `state.subscribe {include}`.
type Include struct {
	Stats  bool
	Health bool
}

// Demand reports what the daemon's subscribers currently want. Returning zero
// subscribers stops the loop until Wake is called.
type Demand func() (subscribers int, include Include)

// Publisher receives every published change. prev is the snapshot the delta is
// computed against; next is the new state. Events are already derived.
type Publisher func(prev, next state.Snapshot, events []state.Event)

// Options configures a Loop. Only Scan is defaulted; the rest may be zero.
type Options struct {
	DaemonVersion string
	Logger        *slog.Logger
	Demand        Demand
	Publish       Publisher

	// Scan overrides the OS scan. Tests inject a fake; production leaves it nil
	// and gets ports.Scan + docker.EnrichPorts + ports.Enrich.
	Scan func(include Include) ([]ports.ListeningPort, error)

	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Loop is the scan goroutine plus the cached snapshot RPC reads serve from.
type Loop struct {
	opts Options
	now  func() time.Time

	wake chan struct{}

	mu           sync.Mutex
	snap         state.Snapshot
	haveSnap     bool
	lastScanAt   time.Time
	lastHealthAt time.Time
	interval     time.Duration
	seq          uint64
	scans        int64
	lastErr      error
}

// New builds a Loop. It does not start scanning; call Run.
func New(opts Options) *Loop {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Demand == nil {
		opts.Demand = func() (int, Include) { return 0, Include{} }
	}
	if opts.Publish == nil {
		opts.Publish = func(state.Snapshot, state.Snapshot, []state.Event) {}
	}
	if opts.Scan == nil {
		opts.Scan = osScan
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Loop{
		opts:     opts,
		now:      now,
		wake:     make(chan struct{}, 1),
		interval: BaseInterval,
	}
}

// osScan is the production scan: the same pipeline `sonar list` runs, so the
// daemon and the no-daemon path emit byte-identical rows.
func osScan(include Include) ([]ports.ListeningPort, error) {
	pp, err := ports.Scan()
	if err != nil {
		return nil, err
	}
	docker.EnrichPorts(pp)
	ports.Enrich(pp)
	if include.Stats {
		ports.EnrichStats(pp, docker.AllContainerStatsAsEntries())
	}
	return pp, nil
}

// SetDemand installs the demand callback after construction. The daemon uses
// it because the server and the loop refer to each other.
func (l *Loop) SetDemand(d Demand) {
	if d != nil {
		l.opts.Demand = d
	}
}

// SetPublisher installs the publish callback after construction.
func (l *Loop) SetPublisher(p Publisher) {
	if p != nil {
		l.opts.Publish = p
	}
}

// Cached returns the last published snapshot without scanning. state.subscribe
// uses it so the reply can be queued atomically with the subscription.
func (l *Loop) Cached() state.Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.haveSnap {
		return state.Snapshot{
			At:            l.now().Format(time.RFC3339),
			DaemonVersion: l.opts.DaemonVersion,
			Ports:         []state.Port{},
			Groups:        []state.Group{},
			Tunnels:       []state.Tunnel{},
			Proxies:       []state.Proxy{},
			Sessions:      []state.SessionRecord{},
		}
	}
	return l.snap
}

// Wake nudges a stopped or backed-off loop to scan now. Called when a
// subscriber connects or when an RPC reads state.
func (l *Loop) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run drives the loop until ctx is cancelled. With zero subscribers it parks on
// Wake and does no work at all; the next RPC or subscription starts it again.
func (l *Loop) Run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		subs, include := l.opts.Demand()
		if subs == 0 {
			// Parked: no scanning, no timer.
			select {
			case <-ctx.Done():
				return
			case <-l.wake:
				continue
			}
		}

		l.scanAndPublish(include)

		l.mu.Lock()
		wait := l.interval
		l.mu.Unlock()

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-l.wake:
			// A read or a new subscriber: back to the base interval.
			l.mu.Lock()
			l.interval = BaseInterval
			l.mu.Unlock()
		}
	}
}

// scanAndPublish runs one scan, updates the cache, adapts the interval and
// publishes when something changed.
func (l *Loop) scanAndPublish(include Include) {
	next, prev, changed, err := l.scanLocked(include)
	if err != nil {
		l.opts.Logger.Warn("scan failed, keeping last good state", "error", err)
		l.opts.Publish(prev, prev, []state.Event{{
			Kind: "scan_error",
			At:   l.nowRFC3339(),
			Data: map[string]any{"error": err.Error()},
		}})
		return
	}
	if !changed {
		return
	}
	l.opts.Publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
}

// scanLocked performs a scan and swaps it into the cache. It returns the new
// snapshot, the one it replaced, and whether anything a client cares about
// changed.
func (l *Loop) scanLocked(include Include) (next, prev state.Snapshot, changed bool, err error) {
	wantHealth := include.Health && l.healthDue()

	pp, err := l.opts.Scan(include)
	if err != nil {
		l.mu.Lock()
		l.lastErr = err
		l.lastScanAt = l.now()
		l.scans++
		prev = l.snap
		l.mu.Unlock()
		return state.Snapshot{}, prev, false, err
	}

	if wantHealth {
		ports.EnrichHealth(pp, HealthTimeout)
	}

	rows := state.FromListeningAll(pp)

	l.mu.Lock()
	defer l.mu.Unlock()

	prev = l.snap
	if include.Health && !wantHealth {
		// Between health cadences, carry the previous probe results forward so
		// a port does not flicker between "checked" and "unknown".
		carryHealth(prev.Ports, rows)
	}

	l.scans++
	l.lastScanAt = l.now()
	l.lastErr = nil
	if wantHealth {
		l.lastHealthAt = l.lastScanAt
	}

	next = state.Snapshot{
		At:            l.lastScanAt.Format(time.RFC3339),
		DaemonVersion: l.opts.DaemonVersion,
		Ports:         rows,
		// Groups arrive with the resolver in step 1A.2; the other three
		// collections belong to specs 2 and 3. They are always arrays.
		Groups:   []state.Group{},
		Tunnels:  []state.Tunnel{},
		Proxies:  []state.Proxy{},
		Sessions: []state.SessionRecord{},
	}

	if l.haveSnap && !snapshotChanged(prev, next, include.Stats) {
		// Nothing to publish: keep the previous seq and back the interval off.
		next.Seq = prev.Seq
		l.snap = next
		l.interval = backoff(l.interval)
		return next, prev, false, nil
	}

	l.seq++
	next.Seq = l.seq
	l.snap = next
	l.haveSnap = true
	l.interval = BaseInterval
	return next, prev, true, nil
}

// healthDue reports whether the health cadence has elapsed.
func (l *Loop) healthDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHealthAt.IsZero() || l.now().Sub(l.lastHealthAt) >= HealthCadence
}

// backoff multiplies the interval by 1.5, capped at MaxInterval.
func backoff(d time.Duration) time.Duration {
	next := time.Duration(float64(d) * BackoffFactor)
	if next > MaxInterval {
		return MaxInterval
	}
	if next < BaseInterval {
		return BaseInterval
	}
	return next
}

// snapshotChanged reports whether the diff between two snapshots is non-empty.
func snapshotChanged(prev, next state.Snapshot, withStats bool) bool {
	d := state.Diff(prev, next)
	if withStats {
		d = state.DiffWithStats(prev, next)
	}
	return len(d.Ports.Added) > 0 || len(d.Ports.Updated) > 0 || len(d.Ports.Removed) > 0 ||
		len(d.Groups.Added) > 0 || len(d.Groups.Updated) > 0 || len(d.Groups.Removed) > 0
}

// carryHealth copies health results from the previous snapshot onto rows that
// were not probed this tick.
func carryHealth(prev []state.Port, next []state.Port) {
	if len(prev) == 0 {
		return
	}
	byKey := make(map[string]*state.Health, len(prev))
	for i := range prev {
		byKey[prev[i].Key()] = prev[i].Health
	}
	for i := range next {
		if next[i].Health == nil {
			if h, ok := byKey[next[i].Key()]; ok {
				next[i].Health = h
			}
		}
	}
}

// deriveEvents turns a snapshot transition into the discrete events the tray
// and app show as notifications.
func deriveEvents(prev, next state.Snapshot, at string) []state.Event {
	d := state.DiffWithStats(prev, next)

	// A key that is both removed and added in one delta is a restart.
	removed := make(map[string]bool, len(d.Ports.Removed))
	for _, k := range d.Ports.Removed {
		removed[k] = true
	}

	events := make([]state.Event, 0, len(d.Ports.Added)+len(d.Ports.Removed))
	restarted := make(map[string]bool, len(d.Ports.Added))
	for i := range d.Ports.Added {
		p := d.Ports.Added[i]
		kind := "port_up"
		if removed[p.Key()] {
			kind, restarted[p.Key()] = "port_restarted", true
		}
		events = append(events, state.Event{Kind: kind, At: at, Port: &p, Group: p.Group})
	}
	before := make(map[string]state.Port, len(prev.Ports))
	for _, p := range prev.Ports {
		before[p.Key()] = p
	}
	for _, key := range d.Ports.Removed {
		if restarted[key] {
			continue
		}
		p := before[key]
		events = append(events, state.Event{Kind: "port_down", At: at, Port: &p, Group: p.Group})
	}
	for i := range d.Ports.Updated {
		p := d.Ports.Updated[i]
		old := before[p.Key()]
		if healthStatus(old.Health) != healthStatus(p.Health) {
			events = append(events, state.Event{
				Kind: "health_changed", At: at, Port: &p, Group: p.Group,
				Data: map[string]any{"from": healthStatus(old.Health), "to": healthStatus(p.Health)},
			})
		}
	}
	return events
}

func healthStatus(h *state.Health) string {
	if h == nil {
		return ""
	}
	return h.Status
}

func (l *Loop) nowRFC3339() string { return l.now().Format(time.RFC3339) }

// Snapshot returns the current state for an RPC read. It reuses the cached
// scan when it is younger than CacheTTL and already carries everything the
// caller asked for; otherwise it scans now. Either way it wakes the loop, so
// reading state snaps the interval back to the base (spec, "Scanner loop").
func (l *Loop) Snapshot(include Include) (state.Snapshot, error) {
	l.mu.Lock()
	fresh := l.haveSnap && l.now().Sub(l.lastScanAt) < CacheTTL
	covers := !include.Stats || l.snapHasStats()
	snap := l.snap
	l.mu.Unlock()

	defer l.Wake()

	if fresh && covers {
		return snap, nil
	}

	next, prev, changed, err := l.scanLocked(include)
	if err != nil {
		if prev.Seq > 0 {
			// A failed rescan still serves the last good state.
			return prev, nil
		}
		return state.Snapshot{}, err
	}
	if changed {
		l.opts.Publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
	}
	return next, nil
}

// snapHasStats reports whether the cached snapshot carries stats. Caller holds
// the mutex.
func (l *Loop) snapHasStats() bool {
	for i := range l.snap.Ports {
		if l.snap.Ports[i].Stats != nil {
			return true
		}
	}
	return len(l.snap.Ports) == 0
}

// Status is what `daemon.status` reports about the scanner.
type Status struct {
	LastScanAt time.Time
	IntervalMs int
	Scans      int64
	Seq        uint64
	LastError  error
}

// Status returns the scanner's current counters.
func (l *Loop) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Status{
		LastScanAt: l.lastScanAt,
		IntervalMs: int(l.interval / time.Millisecond),
		Scans:      l.scans,
		Seq:        l.seq,
		LastError:  l.lastErr,
	}
}
