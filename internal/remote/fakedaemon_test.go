package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// fakeDaemon is a remote sonar daemon as the bridge sees it: a newline-framed
// JSON-RPC peer that answers daemon.hello, serves one state.subscribe and then
// pushes whatever the test tells it to push.
//
// It exists so the multiplexer can be tested without SSH, without a
// subprocess and without a second real daemon: the bridge's contract with the
// far side is the wire, and this is the wire.
type fakeDaemon struct {
	t *testing.T

	// protocolVersion is what daemon.hello reports. A test sets it to "2.0.0"
	// to prove the bridge marks the host incompatible.
	protocolVersion string
	daemonVersion   string

	mu       sync.Mutex
	conn     net.Conn
	enc      *rpc.Encoder
	snap     state.Snapshot
	seq      uint64
	closed   bool
	handlers map[string]func(params json.RawMessage) (any, *rpc.Error)

	// subscribed carries one token per accepted state.subscribe, so a test can
	// wait for "the bridge is following this session" without racing the
	// goroutine that dials. A reconnect produces a second token.
	subscribed chan struct{}
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	return &fakeDaemon{
		t:               t,
		protocolVersion: rpc.ProtocolVersion,
		daemonVersion:   "1.2.3",
		snap: state.Snapshot{
			Seq:      1,
			At:       "2026-09-06T10:00:00Z",
			Ports:    []state.Port{},
			Groups:   []state.Group{},
			Tunnels:  []state.Tunnel{},
			Proxies:  []state.Proxy{},
			Sessions: []state.SessionRecord{},
			Hosts:    []state.Host{{Name: state.LocalhostName, Address: state.LocalhostName, Status: state.HostConnected}},
		},
		handlers:   map[string]func(json.RawMessage) (any, *rpc.Error){},
		subscribed: make(chan struct{}, 16),
	}
}

// dialer is the Dialer a test hands the manager: every dial makes a new pipe
// and a new serving goroutine, so a reconnect really is a new session.
func (f *fakeDaemon) dialer() Dialer {
	return func(_ context.Context, _ Host) (io.ReadWriteCloser, error) {
		mine, theirs := net.Pipe()
		f.mu.Lock()
		f.conn, f.enc, f.closed = mine, rpc.NewEncoder(mine), false
		f.mu.Unlock()
		go f.serve(mine)
		return theirs, nil
	}
}

// setSnapshot replaces the state the far side reports and pushes the delta,
// exactly as a real daemon's scan tick would.
func (f *fakeDaemon) setSnapshot(next state.Snapshot) {
	f.mu.Lock()
	prev := f.snap
	f.seq++
	next.Seq = f.snap.Seq + 1
	if next.At == "" {
		next.At = time.Now().UTC().Format(time.RFC3339)
	}
	f.snap = next
	enc, closed := f.enc, f.closed
	f.mu.Unlock()

	if enc == nil || closed {
		return
	}
	d := state.Diff(prev, next)
	raw, err := json.Marshal(d)
	if err != nil {
		f.t.Error(err)
		return
	}
	_ = enc.Encode(rpc.Notification{JSONRPC: rpc.Version, Method: rpc.MethodStateDelta, Params: raw})
}

// pushEvent sends one state.event notification.
func (f *fakeDaemon) pushEvent(ev state.Event) {
	f.mu.Lock()
	enc := f.enc
	f.mu.Unlock()
	if enc == nil {
		return
	}
	raw, _ := json.Marshal(ev)
	_ = enc.Encode(rpc.Notification{JSONRPC: rpc.Version, Method: rpc.MethodStateEvent, Params: raw})
}

// hangUp drops the current connection, which is what a dead SSH process looks
// like from here.
func (f *fakeDaemon) hangUp() {
	f.mu.Lock()
	conn := f.conn
	f.closed = true
	f.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// handle installs a reply for one method, used by the remote.call tests.
func (f *fakeDaemon) handle(method string, fn func(json.RawMessage) (any, *rpc.Error)) {
	f.mu.Lock()
	f.handlers[method] = fn
	f.mu.Unlock()
}

// waitSubscribed blocks until the bridge has subscribed once more than the
// times it already had.
func (f *fakeDaemon) waitSubscribed(t *testing.T) {
	t.Helper()
	select {
	case <-f.subscribed:
	case <-time.After(testTimeout):
		t.Fatal("the bridge never subscribed")
	}
}

func (f *fakeDaemon) serve(conn net.Conn) {
	dec := rpc.NewDecoder(conn, 4<<20)
	for {
		msg, err := dec.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
		if !msg.IsRequest() {
			continue
		}
		result, rpcErr := f.dispatch(msg)
		f.mu.Lock()
		enc := f.enc
		f.mu.Unlock()
		if enc == nil {
			return
		}
		if rpcErr != nil {
			_ = enc.Encode(rpc.Response{JSONRPC: rpc.Version, ID: msg.ID, Error: rpcErr})
			continue
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return
		}
		_ = enc.Encode(rpc.Response{JSONRPC: rpc.Version, ID: msg.ID, Result: raw})
	}
}

func (f *fakeDaemon) dispatch(msg rpc.Message) (any, *rpc.Error) {
	f.mu.Lock()
	handler := f.handlers[msg.Method]
	f.mu.Unlock()
	if handler != nil {
		return handler(msg.Params)
	}

	switch msg.Method {
	case "daemon.hello":
		return rpc.DaemonHelloResult{
			ProtocolVersion: f.protocolVersion,
			DaemonVersion:   f.daemonVersion,
			PID:             4242,
			Capabilities:    []string{"state", "ports.read"},
		}, nil
	case "state.subscribe":
		f.mu.Lock()
		snap := f.snap
		f.mu.Unlock()
		select {
		case f.subscribed <- struct{}{}:
		default:
		}
		return snap, nil
	case "daemon.status":
		return rpc.DaemonStatusResult{PID: 4242}, nil
	default:
		return nil, rpc.NewError(rpc.CodeNotFound, "unknown method "+msg.Method, "")
	}
}
