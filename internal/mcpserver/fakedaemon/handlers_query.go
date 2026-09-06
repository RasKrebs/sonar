package fakedaemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/claims"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/raskrebs/sonar/internal/store"
)

// The methods behind the MCP query tools (step 2A.5): waiting, free ports,
// claims, logs, health, the connection graph, history and sessions.
//
// Each one mirrors the daemon's own handler — the same defaults, the same
// argument validation, the same error codes — over the fixture instead of over
// a scan. Where the daemon probes the machine (a TCP connect, an HTTP GET, a
// `tail`), the fake answers from the fixture: a port is ready when the fixture
// says something is listening on it, healthy when the fixture row says so, and
// its log backlog is generated from its name.

// waitPollInterval is how often the fake re-reads the fixture while a
// ports.wait stream is open. It is far below the daemon's 1 s so a test that
// makes a port appear does not pay for a real poll cadence.
const waitPollInterval = 5 * time.Millisecond

// logBacklog is how many lines the fake pretends every process has written. It
// sits between the tool's default (100) and its maximum (2000) so both the
// truncated and the untruncated case are reachable.
const logBacklog = 250

// registerQuery installs the query half of the protocol.
func (f *Fake) registerQuery() {
	f.Handle("ports.health", f.handlePortsHealth)
	f.Handle("ports.logs", f.handlePortsLogs)
	f.Handle("ports.graph", f.handlePortsGraph)
	f.Handle("ports.history", f.handlePortsHistory)
	f.Handle("sessions.list", f.handleSessionsList)
	f.HandleStream("ports.wait", f.handlePortsWait)

	// One claim book per fake, held by the handlers rather than by the Fake:
	// resetting the handlers resets the book with them.
	book := claims.New(newMemClaims(), claims.Options{Listening: f.listeningSet})
	f.Handle("ports.next", func(raw json.RawMessage) (any, error) {
		return f.portsNext(raw, book)
	})
	f.Handle("claims.acquire", func(raw json.RawMessage) (any, error) {
		var p rpc.ClaimsAcquireParams
		if err := unmarshal(raw, &p); err != nil {
			return nil, err
		}
		ttl := time.Duration(p.TTLSeconds) * time.Second
		if p.TTLSeconds == 0 && p.TTLMs > 0 {
			ttl = time.Duration(p.TTLMs) * time.Millisecond
		}
		res, err := book.Acquire(claims.Request{
			Key: p.Key, Project: p.Project, Worktree: p.Worktree,
			Count: p.Count, TTL: ttl,
		})
		if err != nil {
			return nil, claimsError(err)
		}
		affected := make([]string, 0, len(res.Ports))
		for _, port := range res.Ports {
			affected = append(affected, fmt.Sprintf("%d:", port))
		}
		return rpc.ClaimsAcquireResult{
			MutationResult: rpc.MutationResult{OK: true, Affected: affected},
			Key:            res.Key,
			Ports:          res.Ports,
			ExpiresAt:      res.ExpiresAt.UTC().Format(time.RFC3339),
		}, nil
	})
	f.Handle("claims.release", func(raw json.RawMessage) (any, error) {
		var p rpc.ClaimsReleaseParams
		if err := unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Key == "" {
			return nil, rpc.NewError(rpc.CodeInvalidParams, "key is required",
				`release the key claims.acquire returned, e.g. {"key": "sonar/main"}`)
		}
		n, err := book.Release(p.Key)
		if err != nil {
			return nil, claimsError(err)
		}
		return rpc.ClaimsReleaseResult{OK: true, Released: n}, nil
	})
	f.Handle("claims.list", func(json.RawMessage) (any, error) {
		live, err := book.List()
		if err != nil {
			return nil, claimsError(err)
		}
		if live == nil {
			live = []state.Claim{}
		}
		return rpc.ClaimsListResult{Claims: live}, nil
	})
}

// listeningSet is the fixture as the set of ports something is bound to, which
// is what a claim probe and `ports.next` step over.
func (f *Fake) listeningSet() (map[int]bool, error) {
	out := map[int]bool{}
	for _, row := range f.Fixture().Ports {
		out[row.Port] = true
	}
	return out, nil
}

// handlePortsNext mirrors the daemon: defaults of 3000/65535/1, a run of
// consecutive free ports, and foreign claims counted as occupied.
func (f *Fake) portsNext(raw json.RawMessage, book *claims.Manager) (any, error) {
	var p rpc.PortsNextParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	start, end, count := p.Start, p.End, p.Count
	if start == 0 {
		start = 3000
	}
	if end == 0 {
		end = 65535
	}
	if count == 0 {
		count = 1
	}
	if start < 1 || end > 65535 || start > end {
		return nil, rpc.NewError(rpc.CodeInvalidParams,
			fmt.Sprintf("invalid port range %d-%d", start, end), "")
	}
	if count < 1 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "count must be at least 1", "")
	}

	occupied, _ := f.listeningSet()
	held, err := book.Held()
	if err != nil {
		return nil, claimsError(err)
	}
	for port, key := range held {
		if p.ClaimKey == nil || *p.ClaimKey != key {
			occupied[port] = true
		}
	}

	free := make([]int, 0, count)
	for port := start; port <= end; port++ {
		if occupied[port] {
			free = free[:0]
			continue
		}
		free = append(free, port)
		if len(free) == count {
			return rpc.PortsNextResult{Ports: free}, nil
		}
	}
	return nil, rpc.NewError(rpc.CodeNotFound,
		fmt.Sprintf("no %d consecutive free port(s) in range %d-%d", count, start, end),
		"widen the range")
}

// handlePortsWait streams one chunk per port that becomes ready and ends with
// {ready, timed_out}. Readiness is "the fixture has this port"; with an http
// path, the fixture row must also be healthy, which is how a test asks for the
// difference between a socket that accepts and a server that answers.
func (f *Fake) handlePortsWait(raw json.RawMessage, s *Stream) (any, error) {
	var p rpc.PortsWaitParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Ports) == 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "ports is required",
			`send {"ports": [3000], "timeout_ms": 30000}`)
	}
	if p.TimeoutMs <= 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "timeout_ms must be positive", "")
	}
	wantHTTP := p.HTTP != nil && *p.HTTP != ""

	targets := append([]int{}, p.Ports...)
	sort.Ints(targets)
	pending := map[int]bool{}
	for _, port := range targets {
		pending[port] = true
	}

	ready := []int{}
	deadline := time.NewTimer(time.Duration(p.TimeoutMs) * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(waitPollInterval)
	defer tick.Stop()

	for {
		live := f.Fixture().Ports
		for _, port := range targets {
			if !pending[port] || !f.portReady(live, port, wantHTTP) {
				continue
			}
			delete(pending, port)
			ready = append(ready, port)
			s.Chunk(rpc.PortsWaitChunk{Port: port, ReadyAt: FixtureTime})
		}
		if len(pending) == 0 || (p.Any && len(ready) > 0) {
			return rpc.PortsWaitEnd{Ready: ready, TimedOut: []int{}}, nil
		}

		select {
		case <-s.Cancelled():
			return rpc.PortsWaitEnd{Ready: ready, TimedOut: stillPending(targets, pending)}, nil
		case <-deadline.C:
			return rpc.PortsWaitEnd{Ready: ready, TimedOut: stillPending(targets, pending)}, nil
		case <-tick.C:
		}
	}
}

func (f *Fake) portReady(rows []state.Port, port int, wantHTTP bool) bool {
	for _, row := range rows {
		if row.Port != port {
			continue
		}
		if !wantHTTP {
			return true
		}
		return row.Health != nil && row.Health.Status == state.HealthOK
	}
	return false
}

// handlePortsHealth answers from each row's fixture health. A port nothing is
// listening on still gets a row — the daemon's rule, so a caller asking about a
// fixed list never has to match up indexes — and it reads as a refused probe.
func (f *Fake) handlePortsHealth(raw json.RawMessage) (any, error) {
	var p rpc.PortsHealthParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	rows := f.Fixture().Ports
	byPort := map[int]state.Port{}
	order := []int{}
	for _, row := range rows {
		if _, seen := byPort[row.Port]; !seen {
			byPort[row.Port] = row
			order = append(order, row.Port)
		}
	}
	if len(p.Ports) > 0 {
		order = p.Ports
	}

	results := make([]rpc.PortHealth, 0, len(order))
	for _, port := range order {
		row, listening := byPort[port]
		switch {
		case !listening:
			results = append(results, rpc.PortHealth{Port: port, Status: state.HealthFail, Reason: "refused"})
		case row.Health == nil:
			results = append(results, rpc.PortHealth{Port: port, Status: state.HealthUnknown})
		default:
			results = append(results, rpc.PortHealth{
				Port:      port,
				Status:    row.Health.Status,
				Code:      row.Health.Code,
				LatencyMs: int64(row.Health.LatencyMs),
				Reason:    row.Health.Reason,
			})
		}
	}
	return rpc.PortsHealthResult{Results: results}, nil
}

// handlePortsLogs is the unary half of ports.logs (contract §20): flat
// selector params, `{source, lines, truncated}` back. `follow` is refused
// rather than faked, because nothing above the daemon uses it yet.
func (f *Fake) handlePortsLogs(raw json.RawMessage) (any, error) {
	var p rpc.PortsLogsParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Follow {
		return nil, rpc.NewError(rpc.CodeUnsupported, "the fake daemon does not follow logs", "")
	}
	row, err := resolvePort(f.Fixture().Ports, p.Selector)
	if err != nil {
		return nil, err
	}
	lines := p.Lines
	if lines <= 0 {
		lines = 100
	}

	backlog := LogLines(row, logBacklog)
	if len(backlog) > lines {
		backlog = backlog[len(backlog)-lines:]
	}
	return rpc.PortsLogsResult{
		Source:    LogSource(row),
		Lines:     backlog,
		Truncated: len(backlog) >= lines,
	}, nil
}

// LogSource is where the fake says a port's output comes from: a container log
// for a docker row, a file for everything else, the way the daemon's own
// ladder labels them.
func LogSource(row state.Port) string {
	if row.Docker != nil && row.Docker.Container != "" {
		return "docker:" + row.Docker.Container
	}
	return fmt.Sprintf("/tmp/sonar/%s-%d.log", row.DisplayName, row.PID)
}

// LogLines generates n deterministic log lines for a port, oldest first.
func LogLines(row state.Port, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("%s[%d] line %d", row.DisplayName, row.PID, i))
	}
	return out
}

// handlePortsGraph returns the fixture's connection graph. DefaultGraphEdges
// carries a duplicated edge on purpose: aggregating repeats into a connection
// count is the tool's job, not the daemon's.
func (f *Fake) handlePortsGraph(json.RawMessage) (any, error) {
	return rpc.PortsGraphResult{Connections: DefaultGraphEdges()}, nil
}

// DefaultGraphEdges is the fixture's graph: the vite dev server proxying to
// the api twice, the api talking to postgres, and the gateway in front of the
// api.
func DefaultGraphEdges() []rpc.GraphEdge {
	return []rpc.GraphEdge{
		{FromPort: 5173, FromPID: 4210, FromProcess: "vite", ToPort: 3000, ToPID: 4101, ToProcess: "api"},
		{FromPort: 5173, FromPID: 4210, FromProcess: "vite", ToPort: 3000, ToPID: 4101, ToProcess: "api"},
		{FromPort: 3000, FromPID: 4101, FromProcess: "api", ToPort: 5432, ToPID: 3300, ToProcess: "shop-db"},
		{FromPort: 8080, FromPID: 3301, FromProcess: "shop-gateway", ToPort: 3000, ToPID: 4101, ToProcess: "api"},
	}
}

// handlePortsHistory answers from a generated ring, newest first, honouring
// the daemon's `since` grammar (a duration or an RFC 3339 timestamp) and its
// port and limit filters.
func (f *Fake) handlePortsHistory(raw json.RawMessage) (any, error) {
	var p rpc.PortsHistoryParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	since, err := parseSince(p.Since)
	if err != nil {
		return nil, err
	}

	events := []rpc.HistoryEvent{}
	for _, e := range HistoryEvents(time.Now()) {
		at, perr := time.Parse(time.RFC3339, e.At)
		if perr != nil {
			continue
		}
		if !since.IsZero() && at.Before(since) {
			continue
		}
		if p.Port != nil && *p.Port != 0 && e.Port != *p.Port {
			continue
		}
		events = append(events, e)
	}
	if p.Limit > 0 && len(events) > p.Limit {
		events = events[:p.Limit]
	}
	return rpc.PortsHistoryResult{Events: events}, nil
}

// HistoryEvents is the fixture's history ring, newest first and stamped
// relative to now so a `since` window means the same thing whenever the tests
// run: one event an hour old, one two hours old, one from the day before.
func HistoryEvents(now time.Time) []rpc.HistoryEvent {
	stamp := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339) }
	return []rpc.HistoryEvent{
		{At: stamp(time.Hour), Kind: "opened", Port: 3000, PID: 4101, DisplayName: "api", Group: "shop"},
		{At: stamp(2 * time.Hour), Kind: "closed", Port: 5173, PID: 4209, DisplayName: "vite", Group: "shop"},
		{At: stamp(30 * time.Hour), Kind: "opened", Port: 5432, PID: 3300, DisplayName: "shop-db", Group: "shop-infra"},
	}
}

// parseSince is the daemon's grammar for the history window (contract §22).
func parseSince(since *string) (time.Time, error) {
	if since == nil || strings.TrimSpace(*since) == "" {
		return time.Time{}, nil
	}
	raw := strings.TrimSpace(*since)
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, rpc.NewError(rpc.CodeInvalidParams,
		"since must be an RFC 3339 timestamp or a duration like 24h, got "+raw, "")
}

func (f *Fake) handleSessionsList(raw json.RawMessage) (any, error) {
	var p rpc.SessionsListParams
	if err := unmarshal(raw, &p); err != nil {
		return nil, err
	}
	out := []state.SessionRecord{}
	for _, s := range f.Fixture().Sessions {
		if p.ActiveOnly && !s.Active {
			continue
		}
		out = append(out, s)
	}
	return rpc.SessionsListResult{Sessions: out}, nil
}

// claimsError maps the manager's failures onto the contract §2 codes, the same
// way the daemon's handler does (contract §28).
func claimsError(err error) error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), claims.ErrExhausted.Error()):
		return rpc.NewError(rpc.CodeNotFound, err.Error(),
			"release stale claims with `sonar claim --release` or widen the range")
	case strings.Contains(err.Error(), claims.ErrNoProject.Error()):
		return rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			`send {"project": "myapp", "worktree": "main"} or {"key": "myapp/main"}`)
	default:
		return rpc.NewError(rpc.CodeInternal, err.Error(), "")
	}
}

// memClaims is claims.Table in memory: the fake's stand-in for the SQLite
// table, so the claim behaviour under test is the real manager's.
type memClaims struct {
	mu   sync.Mutex
	rows map[int]store.ClaimRow
}

func newMemClaims() *memClaims { return &memClaims{rows: map[int]store.ClaimRow{}} }

func (m *memClaims) Put(rows ...store.ClaimRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		if held, ok := m.rows[r.Port]; ok && held.Key != r.Key {
			return store.ErrClaimed
		}
	}
	for _, r := range rows {
		m.rows[r.Port] = r
	}
	return nil
}

func (m *memClaims) Get(key string) ([]store.ClaimRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.ClaimRow{}
	for _, r := range m.rows {
		if r.Key == key {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

func (m *memClaims) List() ([]store.ClaimRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.ClaimRow, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

func (m *memClaims) Delete(key string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for port, r := range m.rows {
		if r.Key == key {
			delete(m.rows, port)
			n++
		}
	}
	return n, nil
}

func (m *memClaims) Expire(now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for port, r := range m.rows {
		if !r.ExpiresAt.IsZero() && r.ExpiresAt.Before(now) {
			delete(m.rows, port)
			n++
		}
	}
	return n, nil
}
