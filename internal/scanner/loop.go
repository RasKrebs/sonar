// Package scanner owns the daemon's single scan goroutine. It wraps the
// existing ports/docker enrichment pipeline, keeps the last good Snapshot, and
// publishes (previous, next) pairs so the daemon can compute whichever delta
// flavours its subscribers asked for.
package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/hoststats"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// Timing constants from the spec's "Scanner loop" section.
const (
	// BaseInterval is the interval while anything is changing.
	BaseInterval = 2 * time.Second
	// MaxInterval caps the backoff on unchanged scans while the daemon is
	// serving RPC reads only.
	MaxInterval = 10 * time.Second
	// SubscribedMaxInterval caps the backoff while at least one subscriber is
	// connected, so a live view never lags a change by more than this.
	SubscribedMaxInterval = 5 * time.Second
	// BackoffFactor multiplies the interval after an unchanged scan.
	BackoffFactor = 1.5
	// CacheTTL is how long an RPC read may reuse the last scan.
	CacheTTL = 2 * time.Second
	// HealthCadence keeps HTTP probes off the port-scan cadence.
	HealthCadence = 10 * time.Second
	// HealthTimeout bounds a single probe.
	HealthTimeout = 2 * time.Second
	// HostStatsTimeout bounds one collection of the machine's own load.
	HostStatsTimeout = 3 * time.Second
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
	// ProtocolVersion is the wire version published on the localhost row of
	// the `hosts` collection. The daemon sets it; a bare Loop leaves it empty.
	ProtocolVersion string
	Logger          *slog.Logger
	Demand          Demand
	Publish         Publisher

	// Store persists renames, group pins, known `.sonar.yaml` roots and the
	// port history ring. Nil means the loop scans without a database.
	Store Store

	// Runs returns the run registry group attribution consults. It is a
	// function because `sonar start` installs the registry after the loop is
	// built; nil, or a nil return, means groups.PortRuns{}.
	Runs func() groups.Registry

	// Sessions builds the snapshot's `sessions` collection from the ports the
	// tick just resolved. It is a function for the same reason Runs is: the
	// daemon installs it from an OnStart hook, after the loop exists. Nil
	// publishes an empty collection.
	Sessions func(ports []state.Port) []state.SessionRecord

	// HostStats collects the machine's own load once per tick — cpu, load,
	// memory, disk, uptime. Tests inject a fake; production leaves it nil and
	// gets hoststats.Collect, whose CPU percent is a delta across the scan
	// interval and therefore needs the same collector every tick.
	HostStats func(ctx context.Context) (state.Host, error)

	// Remote returns the rows multiplexed in from the registered remote hosts,
	// already tagged with their host name (step 3A.2). It is a function
	// because the connection manager is installed from an OnStart hook, after
	// the loop exists; nil means "localhost only", which is what a daemon with
	// no registered hosts publishes.
	Remote func() state.Rows

	// Scan overrides the OS scan. Tests inject a fake; production leaves it nil
	// and gets ports.Scan + docker.EnrichPorts + ports.Enrich.
	Scan func(include Include) ([]ports.ListeningPort, error)

	// Graph overrides the OS lookup of established connections between
	// listening ports. Tests inject a fake; production leaves it nil and gets
	// ports.BuildGraph + docker.BuildDockerGraph.
	//
	// It is a seam for the same reason Scan is. `ports.graph` and
	// `ports.inspect` answer from the snapshot for the listeners, but the
	// connections between them are a second, unrelated trip to the OS —
	// `netstat -ano` on Windows, `lsof` on macOS, `ss` on Linux, plus a
	// `docker inspect` whenever a container is listening. Left un-injectable,
	// a unit test of the *handler* pays for all of that on whatever machine CI
	// happens to be, and on a Windows runner with no Docker running it is
	// seconds, not milliseconds.
	Graph func(listening []ports.ListeningPort) ([]ports.Connection, error)

	// Probe overrides the health probe. Tests inject a fake; production leaves
	// it nil and gets ports.ProbeHealth. Both the configured-health tick and
	// the `ports.health` / `ports.inspect` handlers go through it, so a test
	// never opens a real socket.
	Probe Probe

	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Loop is the scan goroutine plus the cached snapshot RPC reads serve from.
type Loop struct {
	opts Options
	now  func() time.Time

	wake chan struct{}

	attr attribution

	// scanMu serializes a whole scan — the OS call, the attribution that
	// reads the store, the commit into the cache and the publish — against
	// every other scan. Without it only the commit was serialized, and two
	// scans could overlap: the tick a read woke could load the rename table
	// *before* `ports.rename` wrote to it and still commit *after* the
	// rescan the write triggered, overwriting the renamed snapshot with the
	// stale one and publishing a delta that put the old display_name back.
	// Holding it end to end makes the rule "a scan that starts after a write
	// sees it, and a scan that started before one can never land after it"
	// true by construction, and keeps delta seq order the same as publish
	// order.
	scanMu sync.Mutex

	mu   sync.Mutex
	subs int
	snap state.Snapshot
	// local is the last scan's own rows, before the remote hosts were merged
	// in. RemoteChanged republishes from it, so a remote host's delta costs
	// nothing on the local machine: no OS scan, no attribution, no probes.
	local        state.Rows
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
	if opts.Graph == nil {
		opts.Graph = osGraph
	}
	if opts.Probe == nil {
		opts.Probe = ports.ProbeHealth
	}
	if opts.HostStats == nil {
		opts.HostStats = hoststats.New().Collect
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

// osGraph is the production connection graph: the established links between
// listening ports, plus the ones Docker only knows about.
func osGraph(listening []ports.ListeningPort) ([]ports.Connection, error) {
	edges, err := ports.BuildGraph(listening)
	if err != nil {
		return nil, err
	}
	containerEdges, err := docker.BuildDockerGraph(listening)
	if err != nil {
		return nil, fmt.Errorf("container graph: %w", err)
	}
	return append(edges, containerEdges...), nil
}

// Graph reports the established connections between the given listening ports.
// Callers pass the listeners they already have — from the snapshot — so this
// never re-scans for them.
func (l *Loop) Graph(listening []ports.ListeningPort) ([]ports.Connection, error) {
	return l.opts.Graph(listening)
}

// Probe runs one health probe through the loop's seam.
func (l *Loop) Probe(host string, port int, path string, timeout time.Duration) ports.HealthResult {
	return l.opts.Probe(host, port, path, timeout)
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

// SetRemote installs the remote-rows provider after construction. The remote
// connection manager calls it from its OnStart hook.
func (l *Loop) SetRemote(f func() state.Rows) {
	if f != nil {
		l.opts.Remote = f
	}
}

// remoteRows is the current remote contribution, or nothing.
func (l *Loop) remoteRows() state.Rows {
	if l.opts.Remote == nil {
		return state.Rows{}
	}
	return l.opts.Remote()
}

// Cached returns the last published snapshot without scanning. state.subscribe
// uses it so the reply can be queued atomically with the subscription.
func (l *Loop) Cached() state.Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.haveSnap {
		// Even before the first scan `hosts` names this machine: a client
		// that subscribes at startup must not have to special-case an empty
		// collection to find localhost. The registered remote hosts are
		// already there too, so a subscriber never sees them blink in.
		base := state.Rows{Hosts: []state.Host{l.identityRow()}}
		return base.Append(l.remoteRows()).Normalize().Into(state.Snapshot{
			At:            l.now().Format(time.RFC3339),
			DaemonVersion: l.opts.DaemonVersion,
		})
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
// publishes when something changed. One scan at a time (see scanMu).
func (l *Loop) scanAndPublish(include Include) {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()

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
	l.publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
}

// publish hands the transition to the daemon and then writes its port
// transitions to the history ring, in that order: a client sees the change
// before the disk does (daemon spec, "SQLite").
func (l *Loop) publish(prev, next state.Snapshot, events []state.Event) {
	l.opts.Publish(prev, next, events)
	l.record(events)
}

// scanLocked performs a scan and swaps it into the cache. It returns the new
// snapshot, the one it replaced, and whether anything a client cares about
// changed.
func (l *Loop) scanLocked(include Include) (next, prev state.Snapshot, changed bool, err error) {
	// Read outside the mutex: Demand takes the server's own lock, which the
	// server holds while calling back into the loop.
	subs, _ := l.opts.Demand()
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

	// Resolve every port's group with the pins the store holds, apply the
	// stored renames and build the group collection, all before the snapshot
	// is assembled: what gets published is already attributed and named.
	rows, groupRows := l.attribute(pp)

	sessionRows := l.sessions(rows)

	// The machine's own load, read outside the mutex: on macOS it forks `ps`.
	host := l.collectHost()

	// Configured health comes after attribution because it is the groups that
	// say which port has a `health:` path. It runs on every tick regardless of
	// `include`: a health path in a `.sonar.yaml` is part of what the service
	// is, not an opt-in statistic (step 1A.7).
	probeConfigured(rows, groupRows, l.opts.Probe)

	l.mu.Lock()
	defer l.mu.Unlock()

	l.subs = subs
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

	host.Ports, host.Groups = len(rows), len(groupRows)
	host.LastSeen = l.lastScanAt.Format(time.RFC3339)

	// Tunnels and proxies belong to spec 3. Every collection is always an
	// array, never null.
	local := state.Rows{
		Ports:    rows,
		Groups:   groupRows,
		Sessions: sessionRows,
		Hosts:    []state.Host{host},
	}.Tag(state.LocalhostName).Normalize()
	l.local = local

	next = local.Append(l.remoteRows()).Into(state.Snapshot{
		At:            l.lastScanAt.Format(time.RFC3339),
		DaemonVersion: l.opts.DaemonVersion,
	})

	if l.haveSnap && !snapshotChanged(prev, next, include.Stats) {
		if !hostChanged(prev, next) {
			// Nothing to publish: keep the previous seq and back the interval
			// off.
			next.Seq = prev.Seq
			l.snap = next
			l.interval = backoff(l.interval, l.maxIntervalLocked())
			return next, prev, false, nil
		}
		// Only the machine's own load moved. It is published — host load is
		// state, not an opt-in statistic, so it reaches every subscriber
		// whatever their `include` (contract §22's rule for configured
		// health) — but it does not snap the interval back to the base. CPU
		// percent moves on almost every tick, and letting it reset the
		// backoff would pin a subscribed daemon to a full port scan every two
		// seconds forever.
		l.seq++
		next.Seq = l.seq
		l.snap = next
		l.interval = backoff(l.interval, l.maxIntervalLocked())
		return next, prev, true, nil
	}

	l.seq++
	next.Seq = l.seq
	l.snap = next
	l.haveSnap = true
	l.interval = BaseInterval
	return next, prev, true, nil
}

// RemoteChanged republishes the current state with the remote hosts' rows as
// they are now. It runs no OS scan: the local half of the last tick is reused
// verbatim, so a remote daemon publishing a delta every two seconds costs the
// local machine a diff and a marshal rather than a full port scan.
//
// It takes scanMu like a scan does, so its publish cannot interleave with one
// and delta seq order stays publish order (contract §38). Before the first
// local scan there is nothing to merge into, so it wakes the loop instead and
// the remote rows ride out with the first tick.
func (l *Loop) RemoteChanged() {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()

	l.mu.Lock()
	if !l.haveSnap {
		l.mu.Unlock()
		l.Wake()
		return
	}
	prev := l.snap
	next := l.local.Append(l.remoteRows()).Into(state.Snapshot{
		At:              l.now().Format(time.RFC3339),
		DaemonVersion:   l.opts.DaemonVersion,
		ExposuresActive: prev.ExposuresActive,
	})
	if !snapshotChanged(prev, next, true) && !hostChanged(prev, next) {
		l.mu.Unlock()
		return
	}
	l.seq++
	next.Seq = l.seq
	l.snap = next
	l.mu.Unlock()

	l.publish(prev, next, nil)
}

// collectHost reads this machine's load for the tick. A failure is not a scan
// failure: the row is published anyway, with the reason attached and every
// load field null, because `hosts` always names localhost.
func (l *Loop) collectHost() state.Host {
	ctx, cancel := context.WithTimeout(context.Background(), HostStatsTimeout)
	defer cancel()

	h, err := l.opts.HostStats(ctx)
	if err != nil {
		l.opts.Logger.Warn("host stats failed", "error", err)
		reason := err.Error()
		h.StatusReason = &reason
	}
	h.Name, h.Address, h.Status = state.LocalhostName, state.LocalhostName, state.HostConnected
	h.DaemonVersion, h.ProtocolVersion = l.opts.DaemonVersion, l.opts.ProtocolVersion
	return h
}

// identityRow is the localhost row before any load has been collected: who
// this daemon is, with every measurement still null.
func (l *Loop) identityRow() state.Host {
	return state.Host{
		Name:            state.LocalhostName,
		Address:         state.LocalhostName,
		Status:          state.HostConnected,
		DaemonVersion:   l.opts.DaemonVersion,
		ProtocolVersion: l.opts.ProtocolVersion,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		LastSeen:        l.now().Format(time.RFC3339),
	}
}

// hostChanged reports whether the `hosts` collection moved between two
// snapshots.
func hostChanged(prev, next state.Snapshot) bool {
	d := state.DiffHosts(prev.Hosts, next.Hosts)
	return len(d.Added) > 0 || len(d.Updated) > 0 || len(d.Removed) > 0
}

// healthDue reports whether the health cadence has elapsed.
func (l *Loop) healthDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHealthAt.IsZero() || l.now().Sub(l.lastHealthAt) >= HealthCadence
}

// maxIntervalLocked is the ceiling the backoff may reach right now: 5 s while
// anyone is subscribed, so a live view never lags a change by more than that,
// and the full 10 s when the daemon only answers RPC reads. Caller holds the
// mutex.
func (l *Loop) maxIntervalLocked() time.Duration {
	if l.subs > 0 {
		return SubscribedMaxInterval
	}
	return MaxInterval
}

// backoff multiplies the interval by 1.5, capped at max.
func backoff(d, max time.Duration) time.Duration {
	next := time.Duration(float64(d) * BackoffFactor)
	if next > max {
		return max
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
		len(d.Groups.Added) > 0 || len(d.Groups.Updated) > 0 || len(d.Groups.Removed) > 0 ||
		len(d.Sessions.Added) > 0 || len(d.Sessions.Updated) > 0 || len(d.Sessions.Removed) > 0
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
	defer l.Wake()

	if snap, ok := l.cached(include); ok {
		return snap, nil
	}
	return l.scanNow(include)
}

// Rescan scans now whatever the cache says, publishes the change it finds and
// only then returns. It is what a write path calls: `ports.rename`,
// `groups.assign` and the group config writes need the delta carrying the
// change to be on the wire before their own reply is (contract §18), and a
// kill needs its selectors resolved against what is listening this instant.
//
// It is not Invalidate + Snapshot. That pair has a window between the two
// calls: a tick landing in it refreshes lastScanAt from a scan that began
// before the write, Snapshot then finds the cache "fresh" and serves it, and
// the write reaches no one until the next tick — up to SubscribedMaxInterval
// later. Rescan closes the window by holding scanMu across both halves.
func (l *Loop) Rescan(include Include) (state.Snapshot, error) {
	defer l.Wake()
	return l.scanNow(include)
}

// scanNow is the uncached half of both: one scan under scanMu, published
// before it returns.
func (l *Loop) scanNow(include Include) (state.Snapshot, error) {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()

	next, prev, changed, err := l.scanLocked(l.withDemand(include))
	if err != nil {
		if prev.Seq > 0 {
			// A failed rescan still serves the last good state.
			return prev, nil
		}
		return state.Snapshot{}, err
	}
	if changed {
		l.publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
	}
	return next, nil
}

// cached returns the cached snapshot when it is younger than CacheTTL and
// already carries everything the caller asked for.
func (l *Loop) cached(include Include) (state.Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fresh := l.haveSnap && l.now().Sub(l.lastScanAt) < CacheTTL
	covers := !include.Stats || l.snapHasStats()
	return l.snap, fresh && covers
}

// withDemand widens an RPC caller's include with what the subscribers want.
// A scan started by a read is published like any other, so it has to collect
// what a tick would: without this a `sonar list` in the middle of a subscribed
// session would publish a snapshot with the stats stripped out, and every
// subscriber that opted into them would see them blink away and back.
func (l *Loop) withDemand(include Include) Include {
	_, want := l.opts.Demand()
	return Include{
		Stats:  include.Stats || want.Stats,
		Health: include.Health || want.Health,
	}
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
