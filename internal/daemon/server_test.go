package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// pipeWrite and pipeRead bound how long a test will wait on the in-memory
// connection. They are hang-guards, not latency assertions: a handler that
// never answers has to fail the test rather than hang it until `go test
// -timeout` shoots the whole binary, but a handler that answers in 20 ms on a
// laptop and 4 s on a contended two-core CI runner is not a bug, and a deadline
// that calls it one produces exactly the flake this suite kept seeing — three
// different tests, on three different branches, all failing as "read pipe: i/o
// timeout" at the old 3 s.
//
// net.Pipe is synchronous and unbuffered, so the server's write blocks until
// the test reads; the write deadline needs the same headroom as the read.
const (
	pipeWrite = 30 * time.Second
	pipeRead  = 30 * time.Second
	// settle is how long a test may wait for work the daemon does on its own
	// clock — a scan tick, a delta, a stream ending. The scan loop's base
	// interval is 2 s, so anything under a few of those is a race with the
	// runner rather than a bound on the daemon.
	settle = 30 * time.Second
)

// testHarness is a Server wired to an in-memory connection, so the dispatcher,
// the codec, the subscription registry and the fan-out are exercised together
// without touching the filesystem or the OS scanner.
//
// "Without touching the OS" is load-bearing and is enforced by the seams below:
// Scan, Graph and Probe are all faked, so no unit test spawns netstat, lsof,
// ss, ps, tasklist, powershell or docker, and none opens a real socket. A
// handler that reaches the OS behind those seams is a bug — it makes the test
// measure the runner instead of the handler.
type testHarness struct {
	t    *testing.T
	srv  *Server
	loop *scanner.Loop

	stopLoop context.CancelFunc
	loopDone chan struct{}

	mu    sync.Mutex
	rows  []ports.ListeningPort
	edges []ports.Connection
	probe scanner.Probe
}

// newHarness builds a server whose scan loop is already running, so a change to
// the fake scan result reaches subscribers the way it does in production.
//
// A harness that opens a database must be built *after* the t.TempDir() that
// holds it: cleanups run last-registered-first, and the harness has to stop
// before the directory it writes into is removed.
func newHarness(t *testing.T, ctx context.Context) *testHarness {
	t.Helper()
	h := &testHarness{t: t, loopDone: make(chan struct{})}
	h.probe = refusedProbe
	h.loop = scanner.New(scanner.Options{
		DaemonVersion: "test",
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]ports.ListeningPort{}, h.rows...), nil
		},
		// Graph and Probe are faked for the same reason Scan is. A handler
		// test that let them through would pay for `netstat`/`lsof`/`ss`, a
		// `docker inspect` and a real TCP connect on whatever machine CI is —
		// seconds on a Windows runner — and would then be measuring the runner
		// rather than the handler. See scanner.Options.
		Graph: func([]ports.ListeningPort) ([]ports.Connection, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return append([]ports.Connection{}, h.edges...), nil
		},
		Probe: func(host string, port int, path string, timeout time.Duration) ports.HealthResult {
			h.mu.Lock()
			probe := h.probe
			h.mu.Unlock()
			return probe(host, port, path, timeout)
		},
	})
	h.srv = New(Options{Socket: "/test/sonar.sock", Version: "test", Scanner: h.loop})

	runCtx, stop := context.WithCancel(ctx)
	h.stopLoop = stop
	go func() {
		defer close(h.loopDone)
		h.loop.Run(runCtx)
	}()
	t.Cleanup(h.shutdown)
	return h
}

// shutdown stops everything the harness started, in order, and returns only
// once it has stopped.
//
// Cancelling the test's context asks the loop to stop; it does not wait for it
// to have stopped, and a scan already in flight goes on publishing — which
// records history through the store, into the temp directory t.TempDir is
// removing at that very moment. That race failed as "TempDir RemoveAll
// cleanup: directory not empty", pointing at a SQLite -wal file recreated
// behind the delete. Connections first (their handlers write too), then the
// loop, then the database.
func (h *testHarness) shutdown() {
	h.srv.closeAllConns("test over")
	h.srv.wg.Wait()
	h.stopLoop()
	<-h.loopDone
	h.srv.closeStore()
}

// setRows replaces what the fake scanner returns.
func (h *testHarness) setRows(rows ...ports.ListeningPort) {
	h.mu.Lock()
	h.rows = rows
	h.mu.Unlock()
}

// setEdges replaces the connections the fake graph reports.
func (h *testHarness) setEdges(edges ...ports.Connection) {
	h.mu.Lock()
	h.edges = edges
	h.mu.Unlock()
}

// setProbe replaces the fake health probe.
func (h *testHarness) setProbe(p scanner.Probe) {
	h.mu.Lock()
	h.probe = p
	h.mu.Unlock()
}

// refusedProbe is the harness default: nothing is really listening behind the
// fake rows, so every probe is refused, deterministically and instantly.
func refusedProbe(_ string, _ int, _ string, _ time.Duration) ports.HealthResult {
	return ports.HealthResult{Status: "refused"}
}

// dial attaches a client to the server over an in-memory pipe.
func (h *testHarness) dial(ctx context.Context) *testClient {
	h.t.Helper()
	clientSide, serverSide := net.Pipe()
	h.srv.startConn(ctx, serverSide)
	c := &testClient{t: h.t, conn: clientSide, r: bufio.NewReader(clientSide)}
	h.t.Cleanup(func() { clientSide.Close() })
	return c
}

type testClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	id   int
}

// send writes one request and returns its id.
func (c *testClient) send(method string, params any) string {
	c.t.Helper()
	c.id++
	id := json.RawMessage(itoa(c.id))
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatalf("marshalling params: %v", err)
	}
	c.conn.SetWriteDeadline(time.Now().Add(pipeWrite))
	if err := json.NewEncoder(c.conn).Encode(rpc.Request{
		JSONRPC: rpc.Version, ID: id, Method: method, Params: raw,
	}); err != nil {
		c.t.Fatalf("sending %s: %v", method, err)
	}
	return string(id)
}

// read returns the next message from the daemon.
func (c *testClient) read() rpc.Message {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(pipeRead))
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("reading from daemon: %v", err)
	}
	var m rpc.Message
	if err := json.Unmarshal(line, &m); err != nil {
		c.t.Fatalf("decoding %q: %v", line, err)
	}
	return m
}

// call sends a request and returns the matching response, skipping any
// notifications that arrive first.
func (c *testClient) call(method string, params, out any) *rpc.Error {
	c.t.Helper()
	id := c.send(method, params)
	for {
		m := c.read()
		if !m.IsResponse() || string(m.ID) != id {
			continue
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				c.t.Fatalf("decoding %s result: %v", method, err)
			}
		}
		return nil
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestHelloOverTheCodec runs a real request through the newline codec and the
// dispatcher and checks the handshake the whole protocol hangs off.
func TestHelloOverTheCodec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var hello rpc.DaemonHelloResult
	if e := c.call("daemon.hello", rpc.DaemonHelloParams{
		Client: "cli", ClientVersion: "1.2.3",
	}, &hello); e != nil {
		t.Fatalf("daemon.hello: %v", e)
	}

	if hello.ProtocolVersion != rpc.ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", hello.ProtocolVersion, rpc.ProtocolVersion)
	}
	if hello.DaemonVersion != "test" {
		t.Errorf("daemon_version = %q, want test", hello.DaemonVersion)
	}
	if hello.PID == 0 || hello.StartedAt == "" {
		t.Errorf("hello is missing pid or started_at: %+v", hello)
	}
	if hello.Socket != "/test/sonar.sock" {
		t.Errorf("socket = %q, want /test/sonar.sock", hello.Socket)
	}
	want := Capabilities()
	if len(hello.Capabilities) != len(want) {
		t.Fatalf("capabilities = %v, want %v", hello.Capabilities, want)
	}
	for i := range want {
		if hello.Capabilities[i] != want[i] {
			t.Errorf("capabilities = %v, want %v", hello.Capabilities, want)
		}
	}
}

func TestHelloRequiresAClientName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newHarness(t, ctx).dial(ctx)

	e := c.call("daemon.hello", rpc.DaemonHelloParams{ClientVersion: "1"}, nil)
	if e == nil {
		t.Fatal("daemon.hello with no client succeeded")
	}
	if e.Code != rpc.CodeInvalidParams || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params", e)
	}
}

func TestUnknownMethodIsNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newHarness(t, ctx).dial(ctx)

	e := c.call("ports.teleport", rpc.Empty{}, nil)
	if e == nil {
		t.Fatal("an unknown method succeeded")
	}
	if e.Code != rpc.CodeNotFound || e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}
	if e.Data.Hint == "" {
		t.Error("error carries no hint; contract §2 requires one")
	}
}

func TestInvalidIncludeIsRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newHarness(t, ctx).dial(ctx)

	e := c.call("state.snapshot", map[string]any{"include": []string{"stats", "weather"}}, nil)
	if e == nil {
		t.Fatal("an unknown include was accepted")
	}
	if e.Code != rpc.CodeInvalidParams {
		t.Errorf("error = %+v, want invalid_params", e)
	}
}

func TestSnapshotAndStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.setRows(ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	c := h.dial(ctx)

	var snap state.Snapshot
	if e := c.call("state.snapshot", rpc.StateSnapshotParams{}, &snap); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	if len(snap.Ports) != 1 || snap.Ports[0].Port != 8123 {
		t.Fatalf("snapshot ports = %+v, want one row on 8123", snap.Ports)
	}
	if snap.Groups == nil || snap.Tunnels == nil || snap.Proxies == nil || snap.Sessions == nil {
		t.Error("a snapshot collection marshalled as null; contract §11.2 requires arrays")
	}

	var status rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &status); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	if status.Subscribers != 0 {
		t.Errorf("subscribers = %d before subscribing, want 0", status.Subscribers)
	}
	if status.ScanIntervalMs == 0 || status.LastScanAt == "" {
		t.Errorf("status is missing scanner counters: %+v", status)
	}
}

// TestSubscribeThenDelta is the core streaming contract: subscribe replies with
// the snapshot, and later changes arrive as state.delta notifications.
func TestSubscribeThenDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var snap state.Snapshot
	if e := c.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}
	if h.srv.Subscribers() != 1 {
		t.Fatalf("Subscribers() = %d after subscribe, want 1", h.srv.Subscribers())
	}

	h.setRows(ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	h.loop.Wake()

	var delta state.Delta
	var event state.Event
	deadline := time.Now().Add(settle)
	for (len(delta.Ports.Added) == 0 || event.Kind == "") && time.Now().Before(deadline) {
		m := c.read()
		switch m.Method {
		case rpc.MethodStateDelta:
			json.Unmarshal(m.Params, &delta)
		case rpc.MethodStateEvent:
			json.Unmarshal(m.Params, &event)
		}
	}

	if len(delta.Ports.Added) != 1 || delta.Ports.Added[0].Port != 8123 {
		t.Fatalf("delta = %+v, want 8123 added", delta.Ports)
	}
	if delta.Seq <= snap.Seq {
		t.Errorf("delta seq %d did not advance past the snapshot seq %d", delta.Seq, snap.Seq)
	}
	if event.Kind != "port_up" {
		t.Errorf("event kind = %q, want port_up", event.Kind)
	}

	if e := c.call("state.unsubscribe", rpc.Empty{}, nil); e != nil {
		t.Fatalf("state.unsubscribe: %v", e)
	}
	if h.srv.Subscribers() != 0 {
		t.Errorf("Subscribers() = %d after unsubscribe, want 0", h.srv.Subscribers())
	}
}

// TestIncludeFiltersEnrichments checks that a subscriber which did not ask for
// stats never sees them, even when another subscriber made the scanner collect
// them.
func TestIncludeFiltersEnrichments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := &testHarness{t: t}
	h.loop = scanner.New(scanner.Options{
		DaemonVersion: "test",
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{
				Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3",
				CPUPercent: 1.5, MemoryRSS: 1024,
			}}, nil
		},
	})
	h.srv = New(Options{Socket: "/test/sonar.sock", Version: "test", Scanner: h.loop})

	plain := h.dial(ctx)
	var plainSnap state.Snapshot
	if e := plain.call("state.snapshot", rpc.StateSnapshotParams{}, &plainSnap); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	if plainSnap.Ports[0].Stats != nil {
		t.Errorf("stats leaked to a caller that did not ask: %+v", plainSnap.Ports[0].Stats)
	}

	withStats := h.dial(ctx)
	var statsSnap state.Snapshot
	if e := withStats.call("state.snapshot",
		rpc.StateSnapshotParams{Include: rpc.Include{"stats"}}, &statsSnap); e != nil {
		t.Fatalf("state.snapshot with stats: %v", e)
	}
	if statsSnap.Ports[0].Stats == nil {
		t.Fatal("stats missing for a caller that asked for them")
	}
	if statsSnap.Ports[0].Stats.CPUPercent != 1.5 {
		t.Errorf("cpu_percent = %v, want 1.5", statsSnap.Ports[0].Stats.CPUPercent)
	}
}

// TestQueueOverflowDisconnects is the spec's back-pressure rule: a client that
// stops reading is disconnected, never allowed to block the scanner.
func TestQueueOverflowDisconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)

	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	h.srv.startConn(ctx, serverSide)

	// Register as a subscriber by hand: the point of the test is what happens
	// when nobody drains the socket, so we never read the reply.
	h.srv.subsMu.Lock()
	var conn *Conn
	for _, c := range h.srv.conns {
		conn = c
	}
	conn.subscribed, conn.events = true, true
	h.srv.subsMu.Unlock()

	// The writer goroutine takes one message and blocks on the unread pipe;
	// everything after that piles up in the 256-slot queue.
	for i := 0; i < QueueSize+50; i++ {
		h.srv.broadcastEvent(state.Event{Kind: "port_up", At: "now"})
		select {
		case <-conn.closed:
			// closed is closed just before the connection is deregistered, so
			// give that last step a moment rather than racing it.
			for j := 0; j < 100 && h.srv.Clients() != 0; j++ {
				time.Sleep(time.Millisecond)
			}
			if h.srv.Clients() != 0 {
				t.Fatalf("overflowing client still registered after %d messages", i)
			}
			return
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("client survived %d queued notifications; the %d-slot queue did not disconnect it",
		QueueSize+50, QueueSize)
}

// TestShutdownAnswersBeforeClosing checks that daemon.shutdown replies and
// emits daemon_stopping before the socket goes away.
func TestShutdownAnswersBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var snap state.Snapshot
	if e := c.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	id := c.send("daemon.shutdown", rpc.Empty{})

	sawOK, sawStopping := false, false
	for !sawOK || !sawStopping {
		m := c.read()
		switch {
		case m.IsResponse() && string(m.ID) == id:
			var ok rpc.OKResult
			json.Unmarshal(m.Result, &ok)
			if !ok.OK {
				t.Fatalf("daemon.shutdown returned %+v", ok)
			}
			sawOK = true
		case m.Method == rpc.MethodStateEvent:
			var ev state.Event
			json.Unmarshal(m.Params, &ev)
			if ev.Kind == "daemon_stopping" {
				sawStopping = true
			}
		}
	}
}

// TestOversizeMessageIsRejectedNotFatal checks the 4 MiB framing limit: the
// daemon answers with invalid_params and keeps the connection.
func TestOversizeMessageIsRejectedNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newHarness(t, ctx).dial(ctx)

	go func() {
		c.conn.SetWriteDeadline(time.Now().Add(pipeWrite))
		io.WriteString(c.conn, `{"jsonrpc":"2.0","id":1,"method":"daemon.status","params":{"pad":"`)
		chunk := strings.Repeat("x", 64<<10)
		for written := 0; written < MaxMessageBytes+(1<<20); written += len(chunk) {
			if _, err := io.WriteString(c.conn, chunk); err != nil {
				return
			}
		}
		io.WriteString(c.conn, "\"}}\n")
	}()

	m := c.read()
	if m.Error == nil || m.Error.Code != rpc.CodeInvalidParams {
		t.Fatalf("oversize message produced %+v, want invalid_params", m.Error)
	}

	// The connection survives: the decoder resynced on the next line.
	var status rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &status); e != nil {
		t.Fatalf("daemon.status after an oversize message: %v", e)
	}
}

// TestStreamCancelOnAnUnknownID keeps the contract §1 cancel path honest before
// any streaming method exists to use it.
func TestStreamCancelOnAnUnknownID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newHarness(t, ctx).dial(ctx)

	e := c.call("stream.cancel", rpc.StreamCancel{ID: "nope"}, nil)
	if e == nil || e.Code != rpc.CodeNotFound {
		t.Fatalf("stream.cancel on an unknown id = %+v, want not_found", e)
	}
}

// TestIdleTimeoutStopsTheDaemon and TestKeepaliveDisablesIdleTimeout cover the
// two halves of the idle rule.
func TestIdleTimeoutStopsTheDaemon(t *testing.T) {
	srv := New(Options{Socket: "/test/sonar.sock", IdleTimeout: 40 * time.Millisecond,
		Scanner: scanner.New(scanner.Options{})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.watchIdle(ctx)

	select {
	case <-srv.stopping:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon did not stop after its idle timeout")
	}
}

func TestKeepaliveDisablesIdleTimeout(t *testing.T) {
	srv := New(Options{Socket: "/test/sonar.sock", IdleTimeout: 40 * time.Millisecond,
		Scanner: scanner.New(scanner.Options{})})
	srv.keepalives.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.watchIdle(ctx)

	select {
	case <-srv.stopping:
		t.Fatal("the daemon idled out with a keepalive client connected")
	case <-time.After(300 * time.Millisecond):
	}
}

// nextDelta reads until a state.delta notification arrives and decodes it.
func (c *testClient) nextDelta() state.Delta {
	c.t.Helper()
	var d state.Delta
	m := c.nextNotification(rpc.MethodStateDelta)
	if err := json.Unmarshal(m.Params, &d); err != nil {
		c.t.Fatalf("decoding state.delta: %v", err)
	}
	return d
}

// subscribeAndSettle subscribes and waits for the delta the subscription's own
// wake produces, so a test that needs the ports on screen has them. The
// subscribe reply itself is the cached snapshot and may still be empty.
func (c *testClient) subscribeAndSettle(p rpc.StateSubscribeParams) state.Delta {
	c.t.Helper()
	if e := c.call("state.subscribe", p, nil); e != nil {
		c.t.Fatalf("state.subscribe: %v", e)
	}
	return c.nextDelta()
}
