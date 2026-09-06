// Package fakedaemon is an in-process stand-in for `sonar serve`: a real
// listener speaking the real newline-delimited JSON-RPC codec, answering from a
// fixture instead of from a port scan.
//
// It exists so that everything above the daemon — the MCP server, the CLI's
// daemon routing, the desktop app's Go-side helpers — can be tested against the
// published protocol without a machine that happens to have the right ports
// open. Replies are the same Go types the daemon returns, so a test that
// validates them against docs/schema/protocol.schema.json is validating the
// contract, not a hand-written mock.
//
// Handlers are registered in a map, so a later slice adds `ports.kill` or
// `runs.spawn` by calling Handle rather than by editing this file.
package fakedaemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// Handler answers one method. Returning a *rpc.Error produces a JSON-RPC error
// reply; any other error is reported as `internal`.
type Handler func(params json.RawMessage) (any, error)

// Fake is a listening fake daemon. It is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	fixture  Fixture
	handlers map[string]Handler
	// streamHandlers holds the methods answered with a subscription id and a
	// run of chunks (see stream.go).
	streamHandlers map[string]StreamHandler
	conns          map[*conn]struct{}
	ln             net.Listener
	addr           string
	seq            uint64
	closed         bool

	// Calls counts every request the fake has answered, by method. Tests use
	// it to assert that a read was served once, not once per retry.
	calls sync.Map // string -> *int64

	wg sync.WaitGroup
}

// New builds a fake serving fixture. Call Start to bind an address.
func New(fixture Fixture) *Fake {
	f := &Fake{
		fixture:        fixture,
		handlers:       map[string]Handler{},
		streamHandlers: map[string]StreamHandler{},
		conns:          map[*conn]struct{}{},
		seq:            1,
	}
	f.registerCore()
	return f
}

// Handle registers or replaces the handler for a method. Later slices add
// their own methods this way. Replacing a streaming method makes it unary
// again, so a test can stub one out with a plain reply.
func (f *Fake) Handle(method string, h Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = h
	delete(f.streamHandlers, method)
}

// ResetHandlers restores the built-in method set, dropping anything a test
// registered over it with Handle.
func (f *Fake) ResetHandlers() {
	f.mu.Lock()
	f.handlers = map[string]Handler{}
	f.streamHandlers = map[string]StreamHandler{}
	f.mu.Unlock()
	f.registerCore()
}

// Start binds a fresh address and serves it. The address is a unix socket in a
// short temp directory, or a uniquely named pipe on Windows, so several fakes
// can run in one test binary.
func (f *Fake) Start() error { return f.StartAt(TempAddr()) }

// StartAt binds addr and serves it. Restart after Stop reuses the address, so
// a client's reconnect finds the daemon where it left it.
func (f *Fake) StartAt(addr string) error {
	ln, err := daemon.Listen(addr)
	if err != nil {
		return fmt.Errorf("fakedaemon: listening on %s: %w", addr, err)
	}
	f.mu.Lock()
	f.ln, f.addr, f.closed = ln, addr, false
	f.mu.Unlock()

	f.wg.Add(1)
	go f.accept(ln)
	return nil
}

// Restart binds the address the fake last served, after Stop closed it. It is
// how a test plays "the daemon came back".
func (f *Fake) Restart() error {
	f.mu.Lock()
	addr := f.addr
	f.mu.Unlock()
	if addr == "" {
		return errors.New("fakedaemon: Restart before Start")
	}
	if runtime.GOOS != "windows" {
		_ = os.Remove(addr)
	}
	return f.StartAt(addr)
}

// Addr is the address clients dial (SONAR_SOCKET).
func (f *Fake) Addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addr
}

// Stop closes the listener and every open connection, the way a daemon dying
// looks to a client. The fixture and the address survive, so Restart brings
// the same daemon back.
func (f *Fake) Stop() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	ln := f.ln
	f.ln = nil
	conns := make([]*conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.conns = map[*conn]struct{}{}
	f.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		c.close()
	}
	f.wg.Wait()
}

// Close stops the fake and removes its socket file.
func (f *Fake) Close() {
	f.Stop()
	if addr := f.Addr(); addr != "" && runtime.GOOS != "windows" {
		_ = os.Remove(addr)
	}
}

// Fixture returns the data the fake serves.
func (f *Fake) Fixture() Fixture {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fixture
}

// SetPorts replaces the port table, so a test can change what the next read
// sees without rebuilding the fake.
func (f *Fake) SetPorts(ports []state.Port) {
	f.mu.Lock()
	f.fixture.Ports = ports
	f.mu.Unlock()
}

// Calls reports how many requests for method the fake has answered.
func (f *Fake) Calls(method string) int64 {
	v, ok := f.calls.Load(method)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(v.(*int64))
}

// Push broadcasts a state.delta notification to every connected subscriber.
// The delta is completed first: a caller names only the collection it changed,
// and every other one goes out as the empty arrays the contract requires
// ("all three always marshal as arrays, never null").
func (f *Fake) Push(delta state.Delta) {
	delta = completeDelta(delta)

	f.mu.Lock()
	f.seq++
	delta.Seq = f.seq
	if delta.At == "" {
		delta.At = time.Now().Format(time.RFC3339)
	}
	conns := make([]*conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()

	for _, c := range conns {
		if c.subscribed() {
			c.notify(rpc.MethodStateDelta, delta)
		}
	}
}

// Subscribers counts the connections that have called state.subscribe.
func (f *Fake) Subscribers() int {
	f.mu.Lock()
	conns := make([]*conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()

	n := 0
	for _, c := range conns {
		if c.subscribed() {
			n++
		}
	}
	return n
}

func (f *Fake) accept(ln net.Listener) {
	defer f.wg.Done()
	for {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		c := newConn(f, nc)
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			c.close()
			return
		}
		f.conns[c] = struct{}{}
		f.mu.Unlock()

		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			c.serve()
			f.mu.Lock()
			delete(f.conns, c)
			f.mu.Unlock()
		}()
	}
}

func (f *Fake) dispatch(method string, params json.RawMessage) (any, error) {
	f.mu.Lock()
	h, ok := f.handlers[method]
	f.mu.Unlock()
	if !ok {
		// Contract §15: an unknown method is not_found, not a separate code.
		return nil, rpc.NewError(rpc.CodeNotFound, "unknown method "+method,
			"clients feature-detect on daemon.hello.capabilities")
	}
	f.count(method)
	return h(params)
}

func (f *Fake) count(method string) {
	v, _ := f.calls.LoadOrStore(method, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

// TempAddr returns an address no other fake in this process is using: a unix
// socket in a short directory (macOS caps the path at ~104 bytes, and
// t.TempDir() is long) or a numbered named pipe on Windows.
func TempAddr() string {
	n := addrSeq.Add(1)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\sonar-fake-%d-%d`, os.Getpid(), n)
	}
	dir, err := os.MkdirTemp("", "snrfake")
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "d.sock")
}

var addrSeq atomic.Int64

// completeDelta fills in the empty arrays of every collection a caller left
// zero, so a fake delta has the same shape as a real one.
func completeDelta(d state.Delta) state.Delta {
	d.Ports = completeChange(d.Ports)
	d.Groups = completeChange(d.Groups)
	d.Tunnels = completeChange(d.Tunnels)
	d.Proxies = completeChange(d.Proxies)
	d.Sessions = completeChange(d.Sessions)
	return d
}

func completeChange[T any](c state.Change[T]) state.Change[T] {
	if c.Added == nil {
		c.Added = []T{}
	}
	if c.Updated == nil {
		c.Updated = []T{}
	}
	if c.Removed == nil {
		c.Removed = []string{}
	}
	return c
}
