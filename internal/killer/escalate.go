package killer

import (
	"context"

	"github.com/raskrebs/sonar/internal/state"
)

// escalate implements the daemon spec's "SIGTERM, wait up to --grace (default
// 5s), then SIGKILL if the port is still open". It polls the targets rather
// than sleeping out the whole grace period, so a well-behaved process costs
// one poll interval instead of five seconds. Docker units and units that
// failed to resolve are never escalated.
func (e *engine) escalate(ctx context.Context, units []*unit, opts Options, results []Result) {
	pending := make([]*unit, 0, len(units))
	for _, u := range units {
		if u.err == nil && u.container == "" {
			pending = append(pending, u)
		}
	}

	deadline := e.clock.Now().Add(opts.grace())
	for {
		pending = e.stillUp(pending)
		if len(pending) == 0 {
			return
		}
		if !e.clock.Now().Before(deadline) {
			break
		}
		if ctx != nil && ctx.Err() != nil {
			return
		}
		e.clock.Sleep(pollInterval)
	}

	for _, u := range pending {
		e.hardKill(u, results)
	}
}

// stillUp keeps the units that have not gone away. A unit anchored on a port is
// judged by the port: that is the user-visible promise of `sonar kill 3000`. A
// unit addressed by pid alone is judged by its processes.
func (e *engine) stillUp(units []*unit) []*unit {
	out := units[:0]
	for _, u := range units {
		if u.port > 0 {
			if e.portOpen(u.port, u.bind) {
				out = append(out, u)
			}
			continue
		}
		if e.anyAlive(u) {
			out = append(out, u)
		}
	}
	return out
}

// anyAlive reports whether any process of the unit is still running.
func (e *engine) anyAlive(u *unit) bool {
	if u.pgid != 0 {
		return e.alive(u.pgid)
	}
	for _, pid := range u.pids {
		if e.alive(pid) {
			return true
		}
	}
	return false
}

// hardKill sends SIGKILL to whatever of the unit is left, keeping the
// children-before-parents order, and rewrites the affected rows to sigkill.
func (e *engine) hardKill(u *unit, results []Result) {
	mark := func(row int, err error) {
		results[row].Action = state.ActionSIGKILL
		if err != nil {
			results[row].OK = false
			results[row].Error = err.Error()
			return
		}
		results[row].OK = true
		results[row].Error = ""
	}

	if u.pgid != 0 {
		if len(u.rows) > 0 {
			mark(u.rows[0], e.signalGrp(u.pgid, true))
		}
		return
	}
	for i, pid := range u.pids {
		if i >= len(u.rows) || !e.alive(pid) {
			continue
		}
		mark(u.rows[i], e.signalProc(pid, true))
	}
}
