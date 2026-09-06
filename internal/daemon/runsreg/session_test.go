package runsreg

import (
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/sessions"
	"github.com/raskrebs/sonar/internal/state"
)

func agentSession() state.Session {
	return state.Session{
		ID: "claude-code:abc", Tool: sessions.ToolClaudeCode,
		Label: "ship 2A.4", Worktree: "feature-x", Branch: "feature/x", Detected: true,
	}
}

// A session registered with a run comes back on the run, on the ports the run
// owns, and in the list the daemon's sessions handlers read.
func TestSessionRoundTrip(t *testing.T) {
	r := testRegistry(100)
	started := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	r.Register(Record{
		ID: "run1", PID: 100, Group: "web", Name: "dev", Cmd: "npm run dev",
		Cwd: "/home/me/code/shop", StartedAt: started, Session: agentSession(),
	})

	rec, ok := r.Lookup(100)
	if !ok || rec.Session.ID != "claude-code:abc" {
		t.Fatalf("Lookup = %+v, %v", rec, ok)
	}

	got, ok := r.Session(state.Port{PID: 100})
	if !ok {
		t.Fatal("Session did not attribute the run's own pid")
	}
	if got != agentSession() {
		t.Errorf("Session = %+v, want %+v", got, agentSession())
	}

	live := r.SessionRuns()
	if len(live) != 1 {
		t.Fatalf("SessionRuns = %+v, want one entry", live)
	}
	want := sessions.Live{
		RunID: "run1", PID: 100, Group: "web", Name: "dev", Cmd: "npm run dev",
		Cwd: "/home/me/code/shop", StartedAt: started, Session: agentSession(),
	}
	if live[0] != want {
		t.Errorf("SessionRuns[0] = %+v, want %+v", live[0], want)
	}
}

// A child of the run inherits its session, through the same PPID walk that
// decides the run itself: `npm run dev` -> vite -> esbuild is one session.
func TestSessionFollowsThePPIDWalk(t *testing.T) {
	r := testRegistry(100)
	r.Parents = func() map[int]int { return map[int]int{300: 200, 200: 100, 100: 1} }
	r.Register(Record{ID: "run1", PID: 100, Group: "web", Name: "dev", Session: agentSession()})

	if got, ok := r.Session(state.Port{PID: 300, PPID: 200}); !ok || got.ID != "claude-code:abc" {
		t.Errorf("a grandchild's port got session %+v, %v", got, ok)
	}
}

// A run nobody attributed to a session has none, and neither do its ports:
// a plain shell must not invent a session for every dev server on the machine.
func TestRunsWithoutASessionReportNone(t *testing.T) {
	r := testRegistry(100)
	r.Register(Record{ID: "run1", PID: 100, Group: "web", Name: "dev"})

	if _, ok := r.Session(state.Port{PID: 100}); ok {
		t.Error("a run with no session reported one")
	}
	if live := r.SessionRuns(); len(live) != 0 {
		t.Errorf("SessionRuns = %+v, want empty", live)
	}
	if r.SessionRuns() == nil {
		t.Error("SessionRuns returned nil rather than an empty slice")
	}
}

// A dead run leaves the session list with it, so "active" means what it says.
func TestSessionRunsDropsDeadRuns(t *testing.T) {
	r := testRegistry()
	r.Register(Record{ID: "run1", PID: 100, Session: agentSession()})
	if live := r.SessionRuns(); len(live) != 0 {
		t.Errorf("SessionRuns kept a dead run: %+v", live)
	}
}
