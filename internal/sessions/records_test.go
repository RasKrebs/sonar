package sessions

import (
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/state"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func port(n int, group string, session *state.Session) state.Port {
	p := state.Port{Port: n, BindAddress: "127.0.0.1", PID: 1000 + n, Session: session}
	if group != "" {
		g := group
		p.Group = &g
	}
	return p
}

func sess(id, tool string) state.Session {
	return state.Session{ID: id, Tool: tool, Worktree: "feature-x", Branch: "feature/x"}
}

func find(t *testing.T, recs []state.SessionRecord, id string) state.SessionRecord {
	t.Helper()
	for _, r := range recs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no record for %q in %+v", id, recs)
	return state.SessionRecord{}
}

func TestRecordsCountsRunsPortsAndGroups(t *testing.T) {
	s1 := sess("s1", ToolClaudeCode)
	s2 := sess("s2", ToolCodex)

	live := []Live{
		{RunID: "r1", PID: 11, Group: "web", Session: s1, StartedAt: at("2026-09-01T10:00:00Z")},
		{RunID: "r2", PID: 12, Group: "api", Session: s1, StartedAt: at("2026-09-01T10:01:00Z")},
	}
	known := []Known{
		{Session: s1, FirstSeen: at("2026-09-01T09:00:00Z"), LastSeen: at("2026-09-01T10:01:00Z")},
		{Session: s2, FirstSeen: at("2026-08-30T09:00:00Z"), LastSeen: at("2026-08-30T09:30:00Z")},
	}
	ports := []state.Port{
		port(3000, "web", &s1),
		port(3001, "api", &s1),
		port(5432, "db", nil),
	}

	recs := Records(known, live, ports)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(recs), recs)
	}

	one := find(t, recs, "s1")
	if one.Runs != 2 || one.Ports != 2 || one.Groups != 2 {
		t.Errorf("s1 counts = runs %d, ports %d, groups %d; want 2, 2, 2",
			one.Runs, one.Ports, one.Groups)
	}
	if !one.Active {
		t.Error("s1 has live runs but is not active")
	}
	if one.FirstSeen != "2026-09-01T09:00:00Z" {
		t.Errorf("s1 first_seen = %q", one.FirstSeen)
	}

	two := find(t, recs, "s2")
	if two.Active || two.Runs != 0 || two.Ports != 0 {
		t.Errorf("s2 = %+v, want an inactive session with no runs or ports", two)
	}

	// Active sessions sort first, so `sonar sessions` reads top-down.
	if recs[0].ID != "s1" {
		t.Errorf("active session is not first: %+v", recs)
	}
}

// A port stamped with a session the store has never heard of still shows up:
// the daemon is the authority on what is running, not the database.
func TestRecordsIncludesUnknownSessionsFromPorts(t *testing.T) {
	s := sess("ghost", ToolCursor)
	recs := Records(nil, nil, []state.Port{port(4000, "web", &s)})
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if recs[0].Ports != 1 || !recs[0].Active {
		t.Errorf("record = %+v", recs[0])
	}
}

// A live run's identity is fresher than the row written when the session was
// first recorded: a rebase changes the branch under the same id.
func TestRecordsPrefersTheLiveSessionOverTheStoredOne(t *testing.T) {
	stored := state.Session{ID: "s1", Tool: ToolClaudeCode, Branch: "old"}
	live := state.Session{ID: "s1", Tool: ToolClaudeCode, Branch: "new"}
	recs := Records(
		[]Known{{Session: stored, LastSeen: at("2026-09-01T10:00:00Z")}},
		[]Live{{RunID: "r1", Session: live}}, nil)
	if recs[0].Branch != "new" {
		t.Errorf("branch = %q, want the live run's", recs[0].Branch)
	}
}

func TestRunsOfAndPortsOf(t *testing.T) {
	s1, s2 := sess("s1", ToolClaudeCode), sess("s2", ToolCodex)
	live := []Live{
		{RunID: "late", Session: s1, StartedAt: at("2026-09-01T11:00:00Z")},
		{RunID: "early", Session: s1, StartedAt: at("2026-09-01T10:00:00Z")},
		{RunID: "other", Session: s2},
	}
	got := RunsOf(live, "s1")
	if len(got) != 2 || got[0].RunID != "early" {
		t.Errorf("RunsOf = %+v, want early then late", got)
	}

	ports := PortsOf([]state.Port{port(3000, "", &s1), port(3001, "", &s2)}, "s2")
	if len(ports) != 1 || ports[0].Port != 3001 {
		t.Errorf("PortsOf = %+v", ports)
	}
	if PortsOf(nil, "s1") == nil {
		t.Error("PortsOf returned nil rather than an empty slice")
	}
}
