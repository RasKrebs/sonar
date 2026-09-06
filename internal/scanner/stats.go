package scanner

import (
	"context"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// runStats is the stats-only tick: a fixed sampler, decoupled from the port
// scan, that refreshes what moves every second — each listening process's cpu
// and memory, and this machine's own load row — and publishes the difference
// through the ordinary per-subscriber `include` filter.
//
// It exists because those two things are the only parts of a snapshot that
// change continuously, and the port scan they used to ride on is deliberately
// adaptive: it backs off to 5 s while a subscriber watches an unchanging
// machine (contract §37), which is right for ports and useless for a load
// meter. Sampling them apart lets the meter run at 1 s without pinning the
// daemon to a full `lsof`/`netstat` scan at the same rate.
//
// It parks whenever nothing is subscribed, so an unsubscribed daemon —
// answering `sonar list` and `daemon status` over RPC — samples nothing at
// all.
func (l *Loop) runStats(ctx context.Context) {
	interval := l.statsInterval()
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		subs, include := l.opts.Demand()
		if subs == 0 {
			select {
			case <-ctx.Done():
				return
			case <-l.statsWake:
				continue
			}
		}

		l.sampleStats(include)

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(interval)
		// Deliberately not woken by l.statsWake here: a read or a new
		// subscriber must not make the sampler fire faster than its own
		// cadence. Only a parked sampler listens for the nudge.
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// statsInterval is the cadence, clamped to something a machine can sustain.
func (l *Loop) statsInterval() time.Duration {
	d := l.opts.StatsInterval
	if d <= 0 {
		return StatsInterval
	}
	if d < MinStatsInterval {
		return MinStatsInterval
	}
	return d
}

// sampleStats runs one stats-only tick and publishes what moved.
//
// It holds scanMu for exactly the reason a scan does (contract §38): a sample
// must never interleave with a scan's commit, or the two would race to replace
// the cached snapshot and `seq` order would stop matching publish order. It
// never touches the scan interval, the scan counters or `lastScanAt`: the port
// scan's adaptive cadence, the RPC cache TTL and `daemon status` all read
// exactly what they read before this tick existed.
func (l *Loop) sampleStats(include Include) {
	// The publish happens under scanMu too, exactly as a scan's does. A tick
	// that took its seq inside the lock and then published outside it can be
	// overtaken by the next scan, and a subscriber sees seq 35 before seq 34
	// — the one thing §38 promises will not happen.
	l.scanMu.Lock()
	defer l.scanMu.Unlock()

	prev, next, changed := l.sampleStatsLocked(include)
	if !changed {
		return
	}
	// No events: a stats tick cannot bring a port up or down, and nothing it
	// moves belongs in the history ring.
	l.opts.Publish(prev, next, nil)
}

// sampleStatsLocked builds the refreshed snapshot and commits it. Caller holds
// scanMu.
func (l *Loop) sampleStatsLocked(include Include) (prev, next state.Snapshot, changed bool) {
	l.mu.Lock()
	prev, have := l.snap, l.haveSnap
	l.mu.Unlock()
	if !have {
		// Nothing has been scanned yet, so there is no port set to refresh
		// and no identity row to attach a load to. The first scan owns that.
		return prev, prev, false
	}

	// Both of these may fork `ps`; neither may hold l.mu while it does.
	var samples map[int]ports.ProcSample
	if include.Stats {
		samples = l.opts.SampleStats(statsPIDs(prev.Ports))
	}
	host := l.collectHost()

	at := l.now()
	next = prev
	next.At = at.Format(time.RFC3339)
	next.Ports = refreshStats(prev.Ports, samples)
	host.Ports, host.Groups = len(next.Ports), len(next.Groups)
	host.LastSeen = next.At
	next.Hosts = []state.Host{host}

	changed = statsChanged(prev.Ports, next.Ports) || hostChanged(prev, next)

	l.mu.Lock()
	defer l.mu.Unlock()
	if !changed {
		// Commit anyway, so the next tick diffs against the newest reading,
		// but keep the seq: a snapshot nobody was told about must not consume
		// a sequence number (contract §15's resync rule).
		next.Seq = prev.Seq
		l.snap = next
		return prev, next, false
	}
	l.seq++
	next.Seq = l.seq
	l.snap = next
	return prev, next, true
}

// statsPIDs are the processes a stats tick samples: the ones the last snapshot
// already named, minus containers, whose stats come from Docker rather than
// from the process table. A pid that has since exited is simply missing from
// the sample.
func statsPIDs(pp []state.Port) []int {
	pids := make([]int, 0, len(pp))
	for i := range pp {
		if pp[i].Type == state.TypeDocker || pp[i].PID <= 0 || pp[i].Stats == nil {
			continue
		}
		pids = append(pids, pp[i].PID)
	}
	return pids
}

// refreshStats returns the previous rows with only their `stats` object
// replaced. Every other field — display name, group, health, started_at — is
// carried through untouched, which is what makes a stats delta invisible to a
// subscriber that did not ask for stats.
//
// A row the sample has nothing to say about keeps the stats it had: a pid that
// vanished between the scan and this tick is dropped from the sample, not
// zeroed on the wire, and the next port scan is what removes the row.
// `connections` is carried forward too — counting them is an `lsof` (or `ss`,
// or `netstat`) per port and stays on the scan tick.
func refreshStats(prev []state.Port, samples map[int]ports.ProcSample) []state.Port {
	if len(samples) == 0 {
		return prev
	}
	out := make([]state.Port, len(prev))
	copy(out, prev)
	for i := range out {
		if out[i].Stats == nil || out[i].Type == state.TypeDocker {
			continue
		}
		s, ok := samples[out[i].PID]
		if !ok {
			continue
		}
		st := *out[i].Stats // copy: the previous snapshot's object is shared
		st.CPUPercent = s.CPUPercent
		st.MemoryRSS = s.MemoryRSS
		st.Uptime = s.Uptime
		st.State = s.State
		if s.ThreadCount > 0 {
			st.ThreadCount = s.ThreadCount
		}
		out[i].Stats = &st
	}
	return out
}

// statsChanged reports whether any row's stats object moved. It compares the
// slices directly rather than going through state.DiffWithStats because a
// stats tick changes nothing else by construction, and the answer decides
// whether a delta is published at all.
func statsChanged(prev, next []state.Port) bool {
	if len(prev) != len(next) {
		return true
	}
	for i := range next {
		a, b := prev[i].Stats, next[i].Stats
		switch {
		case a == nil && b == nil:
		case a == nil || b == nil:
			return true
		case *a != *b:
			return true
		}
	}
	return false
}
