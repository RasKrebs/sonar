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
// It never touches the scan interval, the scan counters or `lastScanAt`: the
// port scan's adaptive cadence, the RPC cache TTL and `daemon status` all read
// exactly what they read before this tick existed.
func (l *Loop) sampleStats(include Include) {
	// commitMu, not scanMu: see the comment on the fields. It is held across
	// the whole read-sample-commit-publish, not just the commit — a tick that
	// read the snapshot, sampled, and only then took the lock could republish
	// a port set a scan had already replaced in between, which is exactly the
	// revert 1A.15 fixed for scans. Sampling is ~40 ms, so a scan waiting
	// behind it waits ~40 ms.
	l.commitMu.Lock()
	defer l.commitMu.Unlock()

	prev, next, changed := l.sampleStatsLocked(include)
	if !changed {
		return
	}
	// No events: a stats tick cannot bring a port up or down, and nothing it
	// moves belongs in the history ring.
	l.opts.Publish(prev, next, nil)
}

// sampleStatsLocked builds the refreshed snapshot and commits it. Caller holds
// commitMu.
//
// It rebuilds from l.local — this machine's own rows, before the remote hosts
// were merged in — for the same reason RemoteChanged does: only localhost's
// processes are sampled here, and re-merging the remote contribution keeps a
// remote host's rows and its `hosts` entry intact across a local stats tick.
func (l *Loop) sampleStatsLocked(include Include) (prev, next state.Snapshot, changed bool) {
	l.mu.Lock()
	prev, local, have := l.snap, l.local, l.haveSnap
	l.mu.Unlock()
	if !have {
		// Nothing has been scanned yet, so there is no port set to refresh
		// and no identity row to attach a load to. The first scan owns that.
		return prev, prev, false
	}

	// Both of these may fork `ps`; neither may hold l.mu while it does.
	var samples map[int]ports.ProcSample
	if include.Stats {
		samples = l.opts.SampleStats(statsPIDs(local.Ports))
	}
	host := l.collectHost()

	at := l.now()
	local.Ports = refreshStats(local.Ports, samples)
	host.Ports, host.Groups = len(local.Ports), len(local.Groups)
	host.LastSeen = at.Format(time.RFC3339)
	local.Hosts = []state.Host{host}
	local = local.Tag(state.LocalhostName).Normalize()

	next = local.Append(l.remoteRows()).Into(state.Snapshot{
		At:              at.Format(time.RFC3339),
		DaemonVersion:   l.opts.DaemonVersion,
		ExposuresActive: prev.ExposuresActive,
	})

	// The full comparison, not just the stats one: a remote bridge may have
	// replaced its rows since the last publish without RemoteChanged having
	// run yet, and committing those silently would lose them — the next
	// RemoteChanged would diff against a snapshot that already carries them.
	changed = snapshotChanged(prev, next, true) || hostChanged(prev, next)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.local = local
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

// statsPIDs are the processes a stats tick samples: the ones this machine's
// half of the last snapshot already named, minus containers, whose stats come
// from Docker rather than from the process table. A pid that has since exited
// is simply missing from the sample. Remote hosts are never sampled here —
// their pids belong to another machine's process table, and their own daemon
// runs this same tick for them.
func statsPIDs(pp []state.Port) []int {
	pids := make([]int, 0, len(pp))
	for i := range pp {
		if pp[i].Type == state.TypeDocker || pp[i].PID <= 0 || pp[i].Stats == nil {
			continue
		}
		if !state.IsLocalhost(pp[i].Host) {
			continue
		}
		pids = append(pids, pp[i].PID)
	}
	return pids
}

// refreshStats returns the previous rows with only their `stats` object
// replaced. Every other field — display name, group, health, started_at, the
// host tag — is carried through untouched, which is what makes a stats delta
// invisible to a subscriber that did not ask for stats.
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
