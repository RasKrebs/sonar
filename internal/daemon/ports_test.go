package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
)

// fakeRows is the snapshot every read handler is exercised against: a user
// process, a Docker row, a desktop app and a second bind of the same port.
//
// Groups are not set here: the scan tick resolves them, so a row's group is
// whatever the resolver derives — for the Docker rows, the Compose project.
func fakeRows() []ports.ListeningPort {
	return []ports.ListeningPort{
		{
			Port: 3000, PID: 100, Process: "node", Command: "node server.js",
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
			Cwd: "/home/dev/web",
		},
		{
			Port: 3000, PID: 101, Process: "node", Command: "node server.js",
			BindAddress: "::1", IPVersion: "IPv6", Type: ports.PortTypeUser,
		},
		{
			Port: 5432, PID: 200, Process: "com.docker.backend",
			BindAddress: "0.0.0.0", IPVersion: "IPv4", Type: ports.PortTypeDocker,
			DockerContainer: "db-1", DockerImage: "postgres:17", DockerComposeService: "db",
			DockerComposeProject: "shop",
		},
		{
			Port: 7000, PID: 300, Process: "Figma", IsApp: true,
			BindAddress: "127.0.0.1", IPVersion: "IPv4", Type: ports.PortTypeUser,
		},
		{
			Port: 9000, PID: 400, Process: "python3", BindAddress: "127.0.0.1",
			IPVersion: "IPv4", Type: ports.PortTypeUser,
			Tag: "api", RunID: "run-7", RunRootPID: 399,
		},
	}
}

// newPortsHarness starts a server whose scanner returns fakeRows and returns a
// connected test client.
func newPortsHarness(t *testing.T, ctx context.Context) *testClient {
	t.Helper()
	h := newHarness(t, ctx)
	h.setRows(fakeRows()...)
	return h.dial(ctx)
}

func portNumbers(pp []portRow) []int {
	out := make([]int, len(pp))
	for i := range pp {
		out[i] = pp[i].Port
	}
	return out
}

// portRow decodes only what the list assertions need, so the tests do not
// depend on every field of the published shape.
type portRow struct {
	Port        int     `json:"port"`
	BindAddress string  `json:"bind_address"`
	IPVersion   string  `json:"ip_version"`
	Type        string  `json:"type"`
	IsApp       bool    `json:"is_app"`
	Group       *string `json:"group"`
	Stats       *struct {
		CPUPercent float64 `json:"cpu_percent"`
	} `json:"stats"`
}

type listResult struct {
	Ports []portRow `json:"ports"`
}

func TestPortsListHidesAppsByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	var res listResult
	if e := c.call("ports.list", rpc.PortsListParams{}, &res); e != nil {
		t.Fatalf("ports.list: %v", e)
	}
	for _, p := range res.Ports {
		if p.IsApp {
			t.Fatalf("ports.list returned a desktop app without all: true: %+v", p)
		}
	}
	if got := len(res.Ports); got != 4 {
		t.Fatalf("ports.list returned %d rows, want 4: %v", got, portNumbers(res.Ports))
	}

	var all listResult
	if e := c.call("ports.list", rpc.PortsListParams{All: true}, &all); e != nil {
		t.Fatalf("ports.list all: %v", e)
	}
	if len(all.Ports) != 5 {
		t.Fatalf("ports.list{all} returned %d rows, want 5", len(all.Ports))
	}
}

func TestPortsListFilters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	str := func(s string) *string { return &s }

	for _, tt := range []struct {
		name   string
		params rpc.PortsListParams
		want   []int
	}{
		{"by type", rpc.PortsListParams{Filter: str("docker")}, []int{5432}},
		{"by group", rpc.PortsListParams{Group: str("shop")}, []int{5432}},
		{"by run name", rpc.PortsListParams{Group: str("api")}, []int{9000}},
		{"by run id", rpc.PortsListParams{Group: str("run-7")}, []int{9000}},
		{"by ip version", rpc.PortsListParams{IPVersion: str("IPv6")}, []int{3000}},
		{"by ip shorthand", rpc.PortsListParams{IPVersion: str("6")}, []int{3000}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var res listResult
			if e := c.call("ports.list", tt.params, &res); e != nil {
				t.Fatalf("ports.list: %v", e)
			}
			got := portNumbers(res.Ports)
			if len(got) != len(tt.want) {
				t.Fatalf("got ports %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got ports %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestPortsListRejectsUnknownFilters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	bad := "containers"
	e := c.call("ports.list", rpc.PortsListParams{Filter: &bad}, nil)
	if e == nil || e.Code != rpc.CodeInvalidParams {
		t.Fatalf("unknown filter should be invalid params, got %v", e)
	}
	e = c.call("ports.list", rpc.PortsListParams{IPVersion: &bad}, nil)
	if e == nil || e.Code != rpc.CodeInvalidParams {
		t.Fatalf("unknown ip_version should be invalid params, got %v", e)
	}
}

func TestPortsListHonoursInclude(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	rows := fakeRows()
	rows[0].CPUPercent = 4.5
	h.setRows(rows...)
	c := h.dial(ctx)

	var bare listResult
	if e := c.call("ports.list", rpc.PortsListParams{}, &bare); e != nil {
		t.Fatalf("ports.list: %v", e)
	}
	for _, p := range bare.Ports {
		if p.Stats != nil {
			t.Fatalf("stats leaked to a caller that did not ask: %+v", p)
		}
	}

	var withStats listResult
	if e := c.call("ports.list", rpc.PortsListParams{Include: rpc.Include{"stats"}}, &withStats); e != nil {
		t.Fatalf("ports.list stats: %v", e)
	}
	found := false
	for _, p := range withStats.Ports {
		if p.Stats != nil && p.Stats.CPUPercent == 4.5 {
			found = true
		}
	}
	if !found {
		t.Fatal("include: [stats] did not deliver the collected stats")
	}
}

func TestPortsInspectAmbiguousAndMissing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	port := 3000
	e := c.call("ports.inspect", rpc.Selector{Port: &port}, nil)
	if e == nil || e.Code != rpc.CodeAmbiguous {
		t.Fatalf("a multi-bind port should be ambiguous, got %v", e)
	}

	missing := 1
	e = c.call("ports.inspect", rpc.Selector{Port: &missing}, nil)
	if e == nil || e.Code != rpc.CodeNotFound {
		t.Fatalf("an unlistened port should be not found, got %v", e)
	}

	e = c.call("ports.inspect", rpc.Selector{}, nil)
	if e == nil || e.Code != rpc.CodeInvalidParams {
		t.Fatalf("an empty selector should be invalid params, got %v", e)
	}
}

func TestPortsInspectResolvesByBindAndPID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	port, bind := 3000, "::1"
	var res struct {
		Port        portRow  `json:"port"`
		LogSources  []string `json:"log_sources"`
		Connections []any    `json:"connections"`
	}
	if e := c.call("ports.inspect", rpc.Selector{Port: &port, BindAddress: &bind}, &res); e != nil {
		t.Fatalf("ports.inspect: %v", e)
	}
	if res.Port.BindAddress != "::1" {
		t.Fatalf("bind_address did not disambiguate: %+v", res.Port)
	}
	if res.LogSources == nil || res.Connections == nil {
		t.Fatal("log_sources and connections must always be arrays")
	}

	pid := 200
	if e := c.call("ports.inspect", rpc.Selector{PID: &pid}, &res); e != nil {
		t.Fatalf("ports.inspect by pid: %v", e)
	}
	if res.Port.Port != 5432 {
		t.Fatalf("pid selector picked port %d, want 5432", res.Port.Port)
	}
}

func TestPortsNextSkipsListeningPorts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	var res rpc.PortsNextResult
	if e := c.call("ports.next", rpc.PortsNextParams{Start: 3000, End: 3005}, &res); e != nil {
		t.Fatalf("ports.next: %v", e)
	}
	if len(res.Ports) != 1 || res.Ports[0] != 3001 {
		t.Fatalf("ports.next = %v, want [3001]", res.Ports)
	}

	if e := c.call("ports.next", rpc.PortsNextParams{Start: 2999, End: 3002, Count: 2}, &res); e != nil {
		t.Fatalf("ports.next consecutive: %v", e)
	}
	if len(res.Ports) != 2 || res.Ports[0] != 3001 || res.Ports[1] != 3002 {
		t.Fatalf("ports.next consecutive = %v, want [3001 3002]", res.Ports)
	}

	e := c.call("ports.next", rpc.PortsNextParams{Start: 3000, End: 3000}, nil)
	if e == nil || e.Code != rpc.CodeNotFound {
		t.Fatalf("an exhausted range should be not found, got %v", e)
	}
	e = c.call("ports.next", rpc.PortsNextParams{Start: 4000, End: 3000}, nil)
	if e == nil || e.Code != rpc.CodeInvalidParams {
		t.Fatalf("an inverted range should be invalid params, got %v", e)
	}
}

func TestPortsGraphAndHealthShapes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	var graph rpc.PortsGraphResult
	switch e := c.call("ports.graph", rpc.Empty{}, &graph); {
	case e == nil:
		if graph.Connections == nil {
			t.Fatal("connections must always be an array")
		}
	case e.Code == rpc.CodeInternal:
		// No lsof / ss / docker on this machine: the shape is all this test
		// can check, and there is nothing to check it against.
		t.Log("connection graph unavailable here:", e.Message)
	default:
		t.Fatalf("ports.graph: %v", e)
	}

	// Probing a port nothing is listening on is the deterministic case: it is
	// refused, and the row still comes back so callers can match by index.
	var health rpc.PortsHealthResult
	if e := c.call("ports.health", rpc.PortsHealthParams{Ports: []int{1}}, &health); e != nil {
		t.Fatalf("ports.health: %v", e)
	}
	if len(health.Results) != 1 || health.Results[0].Port != 1 {
		t.Fatalf("ports.health = %+v, want one row for port 1", health.Results)
	}
	if health.Results[0].Status == "" {
		t.Fatal("ports.health returned an empty status")
	}
}

func TestPortsWaitStreamsReadyThenEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	ready := make(chan struct{})
	restore := probeReady
	probeReady = func(port int, _ string) bool {
		if port != 4242 {
			return false
		}
		select {
		case <-ready:
			return true
		default:
			return false
		}
	}
	t.Cleanup(func() { probeReady = restore })

	var start rpc.StreamStart
	if e := c.call("ports.wait", rpc.PortsWaitParams{
		Ports: []int{4242}, TimeoutMs: 5000, IntervalMs: 20,
	}, &start); e != nil {
		t.Fatalf("ports.wait: %v", e)
	}
	if start.SubscriptionID == "" {
		t.Fatal("ports.wait did not return a subscription id")
	}
	close(ready)

	msg := c.nextNotification(rpc.MethodStreamChunk)
	var chunk rpc.StreamChunk
	if err := json.Unmarshal(msg.Params, &chunk); err != nil {
		t.Fatalf("decoding chunk: %v", err)
	}
	var body rpc.PortsWaitChunk
	if err := json.Unmarshal(chunk.Data, &body); err != nil {
		t.Fatalf("decoding wait chunk: %v", err)
	}
	if body.Port != 4242 || body.ReadyAt == "" {
		t.Fatalf("wait chunk = %+v, want port 4242 with a timestamp", body)
	}

	msg = c.nextNotification(rpc.MethodStreamEnd)
	var end rpc.StreamEnd
	if err := json.Unmarshal(msg.Params, &end); err != nil {
		t.Fatalf("decoding stream.end: %v", err)
	}
	var final rpc.PortsWaitEnd
	if err := json.Unmarshal(end.Data, &final); err != nil {
		t.Fatalf("decoding wait end: %v", err)
	}
	if len(final.Ready) != 1 || final.Ready[0] != 4242 || len(final.TimedOut) != 0 {
		t.Fatalf("wait end = %+v, want ready [4242]", final)
	}
}

func TestPortsWaitTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	restore := probeReady
	probeReady = func(int, string) bool { return false }
	t.Cleanup(func() { probeReady = restore })

	var start rpc.StreamStart
	if e := c.call("ports.wait", rpc.PortsWaitParams{
		Ports: []int{4242, 4243}, TimeoutMs: 60, IntervalMs: 10,
	}, &start); e != nil {
		t.Fatalf("ports.wait: %v", e)
	}

	deadline := time.After(3 * time.Second)
	done := make(chan rpc.PortsWaitEnd, 1)
	go func() {
		msg := c.nextNotification(rpc.MethodStreamEnd)
		var end rpc.StreamEnd
		_ = json.Unmarshal(msg.Params, &end)
		var final rpc.PortsWaitEnd
		_ = json.Unmarshal(end.Data, &final)
		done <- final
	}()
	select {
	case final := <-done:
		if len(final.Ready) != 0 || len(final.TimedOut) != 2 {
			t.Fatalf("wait end = %+v, want both ports timed out", final)
		}
	case <-deadline:
		t.Fatal("ports.wait never ended")
	}
}

func TestPortsWaitValidatesParams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	if e := c.call("ports.wait", rpc.PortsWaitParams{TimeoutMs: 100}, nil); e == nil ||
		e.Code != rpc.CodeInvalidParams {
		t.Fatalf("wait without ports should be invalid params, got %v", e)
	}
	if e := c.call("ports.wait", rpc.PortsWaitParams{Ports: []int{3000}}, nil); e == nil ||
		e.Code != rpc.CodeInvalidParams {
		t.Fatalf("wait without a timeout should be invalid params, got %v", e)
	}
	if e := c.call("ports.wait", rpc.PortsWaitParams{Ports: []int{0}, TimeoutMs: 10}, nil); e == nil ||
		e.Code != rpc.CodeInvalidParams {
		t.Fatalf("wait on port 0 should be invalid params, got %v", e)
	}
}

func TestPortsLogsRejectsUnknownPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newPortsHarness(t, ctx)

	missing := 1
	e := c.call("ports.logs", rpc.PortsLogsParams{Selector: rpc.Selector{Port: &missing}}, nil)
	if e == nil || e.Code != rpc.CodeNotFound {
		t.Fatalf("logs for an unlistened port should be not found, got %v", e)
	}
}

func TestPortsReadMethodsShareOneScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.setRows(fakeRows()...)
	c1, c2 := h.dial(ctx), h.dial(ctx)

	var res listResult
	if e := c1.call("ports.list", rpc.PortsListParams{}, &res); e != nil {
		t.Fatalf("ports.list: %v", e)
	}
	after := h.loop.Status().Scans

	// Every read inside the cache TTL is served from the same scan, whichever
	// client asks: that is what keeps `watch` from forking lsof per tick.
	for range 5 {
		if e := c2.call("ports.list", rpc.PortsListParams{}, &res); e != nil {
			t.Fatalf("ports.list: %v", e)
		}
	}
	if got := h.loop.Status().Scans; got != after {
		t.Fatalf("five more reads cost %d extra scans, want 0", got-after)
	}
}

// TestReadsAreFreeWhileSomeoneIsSubscribed is the acceptance demo in miniature:
// with a `watch` connected, the scan loop is already running and a burst of
// `list` calls must not make it run any harder.
func TestReadsAreFreeWhileSomeoneIsSubscribed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.setRows(fakeRows()...)

	watcher := h.dial(ctx)
	var snap map[string]any
	if e := watcher.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	reader := h.dial(ctx)
	var res listResult
	if e := reader.call("ports.list", rpc.PortsListParams{}, &res); e != nil {
		t.Fatalf("ports.list: %v", e)
	}
	before := h.loop.Status().Scans

	// The loop keeps scanning on its own cadence; what must not happen is a
	// read adding scans of its own. Give the reads no time to be "stale".
	for range 20 {
		if e := reader.call("ports.list", rpc.PortsListParams{}, &res); e != nil {
			t.Fatalf("ports.list: %v", e)
		}
		if e := reader.call("ports.next", rpc.PortsNextParams{Start: 9100, End: 9200}, nil); e != nil {
			t.Fatalf("ports.next: %v", e)
		}
	}
	if got := h.loop.Status().Scans; got != before {
		t.Fatalf("forty reads alongside a subscriber cost %d scans, want 0", got-before)
	}
}

func TestDaemonStatusReportsTheScanCounter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.setRows(fakeRows()...)
	c := h.dial(ctx)

	var before rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &before); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	var res listResult
	if e := c.call("ports.list", rpc.PortsListParams{}, &res); e != nil {
		t.Fatalf("ports.list: %v", e)
	}
	var after rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &after); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	if after.Scans <= before.Scans {
		t.Fatalf("scan counter did not move: %d then %d", before.Scans, after.Scans)
	}
}
