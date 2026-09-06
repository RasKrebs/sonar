package scanner

import (
	"testing"

	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/sessions"
	"github.com/raskrebs/sonar/internal/state"
)

// sessionRegistry is a run registry that also knows the agent session, the
// shape internal/daemon/runsreg has once a run carries one.
type sessionRegistry struct {
	pid     int
	session state.Session
}

func (r sessionRegistry) Run(p state.Port) (state.Run, bool) {
	if p.PID != r.pid {
		return state.Run{}, false
	}
	return state.Run{ID: "run1", Group: "shop", Name: "web", RootPID: r.pid}, true
}

func (r sessionRegistry) Session(p state.Port) (state.Session, bool) {
	if p.PID != r.pid {
		return state.Session{}, false
	}
	return r.session, true
}

// A scan tick stamps the session of the owning run onto the port and publishes
// the sessions collection built from it.
func TestScanStampsPortSessionAndPublishesTheCollection(t *testing.T) {
	agent := state.Session{
		ID: "claude-code:abc", Tool: sessions.ToolClaudeCode,
		Worktree: "feature-x", Branch: "feature/x", Detected: true,
	}
	reg := sessionRegistry{pid: 11, session: agent}

	l := New(Options{
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{
				{Port: 3000, PID: 11, Process: "python3", Command: "python3 -m http.server"},
				{Port: 5432, PID: 12, Process: "postgres"},
			}, nil
		},
		Runs: func() groups.Registry { return reg },
	})
	l.SetSessions(func(rows []state.Port) []state.SessionRecord {
		return sessions.Records(nil, []sessions.Live{{RunID: "run1", PID: 11, Group: "shop", Session: agent}}, rows)
	})

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	byPort := map[int]state.Port{}
	for _, p := range snap.Ports {
		byPort[p.Port] = p
	}
	got := byPort[3000].Session
	if got == nil {
		t.Fatal("the run's port was published with no session")
	}
	if *got != agent {
		t.Errorf("port session = %+v, want %+v", *got, agent)
	}
	if byPort[5432].Session != nil {
		t.Errorf("an unrelated port got session %+v", byPort[5432].Session)
	}

	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions collection = %+v, want one record", snap.Sessions)
	}
	rec := snap.Sessions[0]
	if rec.ID != agent.ID || rec.Runs != 1 || rec.Ports != 1 || !rec.Active {
		t.Errorf("session record = %+v", rec)
	}
}

// Without a provider the collection is an empty array, never null: the wire
// shape is fixed whether or not anything owns sessions in this build.
func TestSnapshotSessionsAreNeverNull(t *testing.T) {
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Sessions == nil {
		t.Error("Snapshot.Sessions is nil")
	}
	if cached := l.Cached(); cached.Sessions == nil {
		t.Error("Cached().Sessions is nil")
	}
}

// A session appearing is a change worth publishing even when no port moved:
// the desktop's session badge has to arrive without waiting for a port event.
func TestSessionChangeAlonePublishes(t *testing.T) {
	prev := state.Snapshot{Ports: []state.Port{}, Groups: []state.Group{}}
	next := state.Snapshot{
		Ports:    []state.Port{},
		Groups:   []state.Group{},
		Sessions: []state.SessionRecord{{Session: state.Session{ID: "s1"}, Active: true}},
	}
	if !snapshotChanged(prev, next, false) {
		t.Error("a new session did not count as a change")
	}
}
