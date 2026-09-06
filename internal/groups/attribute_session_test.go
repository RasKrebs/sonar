package groups

import (
	"testing"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// sessionRunSet is a Registry that also implements SessionRegistry, which is
// what the daemon's run registry looks like once a run carries a session.
type sessionRunSet struct {
	runSet
	session state.Session
}

func (s sessionRunSet) Session(p state.Port) (state.Session, bool) {
	if p.Port != s.port {
		return state.Session{}, false
	}
	return s.session, true
}

func TestAttributeWithStampsTheSession(t *testing.T) {
	want := state.Session{
		ID: "claude-code:abc", Tool: "claude-code",
		Worktree: "feature-x", Branch: "feature/x", Detected: true,
	}
	pp := []ports.ListeningPort{
		{Port: 3000, PID: 11, Process: "node", Command: "node server.js"},
		{Port: 4000, PID: 12, Process: "python3", Command: "python3 -m http.server"},
	}

	resolved, _ := AttributeWith(pp, NoPins{},
		sessionRunSet{runSet: runSet{port: 4000, id: "r1", group: "itest", name: "web"}, session: want},
		nil)

	byPort := map[int]state.Port{}
	for _, p := range resolved {
		byPort[p.Port] = p
	}

	got := byPort[4000].Session
	if got == nil {
		t.Fatal("the run's port was published with no session")
	}
	if *got != want {
		t.Errorf("session = %+v, want %+v", *got, want)
	}
	if byPort[4000].Run == nil || byPort[4000].Run.ID != "r1" {
		t.Errorf("the session was stamped but the run was not: %+v", byPort[4000].Run)
	}
	if byPort[3000].Session != nil {
		t.Errorf("an unattributed port got session %+v", byPort[3000].Session)
	}
}

// A registry that knows nothing about sessions publishes null sessions, which
// is the direct-scan path: runs.json has no session column.
func TestAttributeWithoutASessionRegistryLeavesSessionsNull(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "python3"}}
	resolved, _ := AttributeWith(pp, NoPins{},
		runSet{port: 4000, id: "r1", group: "itest", name: "web"}, nil)
	if resolved[0].Session != nil {
		t.Errorf("session = %+v, want nil", resolved[0].Session)
	}
}

// An empty session id is not a session: a registry that answers ok with a zero
// value must not put an empty badge on the row.
func TestAttributeWithIgnoresAnEmptySessionID(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "python3"}}
	resolved, _ := AttributeWith(pp, NoPins{},
		sessionRunSet{runSet: runSet{port: 4000, id: "r1", group: "g", name: "n"}}, nil)
	if resolved[0].Session != nil {
		t.Errorf("session = %+v, want nil", resolved[0].Session)
	}
}
