package sessions

import (
	"sort"
	"time"

	"github.com/raskrebs/sonar/internal/state"
)

// Live is one running run that carries a session, as the daemon's run registry
// reports it. It is the whole run rather than just its id because
// `sessions.inspect` lists the runs of a session and there is no second place
// to look them up from.
type Live struct {
	RunID     string
	PID       int
	Group     string
	Name      string
	Cmd       string
	Cwd       string
	StartedAt time.Time
	Session   state.Session
}

// Known is a session the store remembers, live or not. Inactive sessions are
// kept for the prune window so `port_history` can still say who started a port
// that is long gone.
type Known struct {
	Session   state.Session
	FirstSeen time.Time
	LastSeen  time.Time
}

// Retention is how long an inactive session is kept before Prune drops it
// (spec 2 §3).
const Retention = 7 * 24 * time.Hour

// Records builds the `sessions` collection of the snapshot: every session the
// store knows plus every session that has a live run, each with the counts a
// client renders and whether it is still active.
//
// A session is active while it has at least one live run — not while it has a
// port. An agent whose dev server is still coming up is active, and so is one
// whose command binds nothing at all.
func Records(known []Known, live []Live, ports []state.Port) []state.SessionRecord {
	byID := map[string]*state.SessionRecord{}
	order := []string{}

	add := func(s state.Session) *state.SessionRecord {
		if rec, ok := byID[s.ID]; ok {
			return rec
		}
		rec := &state.SessionRecord{Session: s}
		byID[s.ID] = rec
		order = append(order, s.ID)
		return rec
	}

	for _, k := range known {
		rec := add(k.Session)
		rec.FirstSeen = rfc3339(k.FirstSeen)
		rec.LastSeen = rfc3339(k.LastSeen)
	}

	groupsSeen := map[string]map[string]bool{}
	for _, l := range live {
		if l.Session.ID == "" {
			continue
		}
		rec := add(l.Session)
		// The live run's own identity is fresher than a store row written when
		// the session was first seen: a rebased worktree changes branch.
		rec.Session = l.Session
		rec.Runs++
		rec.Active = true
		if rec.FirstSeen == "" {
			rec.FirstSeen = rfc3339(l.StartedAt)
		}
		if l.Group != "" {
			if groupsSeen[l.Session.ID] == nil {
				groupsSeen[l.Session.ID] = map[string]bool{}
			}
			groupsSeen[l.Session.ID][l.Group] = true
		}
	}

	for i := range ports {
		s := ports[i].Session
		if s == nil || s.ID == "" {
			continue
		}
		rec, ok := byID[s.ID]
		if !ok {
			rec = add(*s)
			rec.Active = true
		}
		rec.Ports++
		if g := ports[i].Group; g != nil && *g != "" {
			if groupsSeen[s.ID] == nil {
				groupsSeen[s.ID] = map[string]bool{}
			}
			groupsSeen[s.ID][*g] = true
		}
	}

	out := make([]state.SessionRecord, 0, len(order))
	for _, id := range order {
		rec := byID[id]
		rec.Groups = len(groupsSeen[id])
		out = append(out, *rec)
	}
	// Newest activity first, so the list reads like a session log; ties break
	// on id so the collection is stable between ticks and the delta is quiet.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RunsOf returns the live runs of one session, oldest first.
func RunsOf(live []Live, id string) []Live {
	var out []Live
	for _, l := range live {
		if l.Session.ID == id {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// PortsOf returns the ports attributed to one session.
func PortsOf(ports []state.Port, id string) []state.Port {
	out := []state.Port{}
	for i := range ports {
		if s := ports[i].Session; s != nil && s.ID == id {
			out = append(out, ports[i])
		}
	}
	return out
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
