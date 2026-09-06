package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// TestDaemonDropMidCallIsADomainError is the recovery story spec 1 asks for:
// the daemon dies while a tool call is in flight, the call comes back as a
// domain error the model can retry rather than a broken tool, and once the
// daemon is back the same call works again.
func TestDaemonDropMidCallIsADomainError(t *testing.T) {
	h := newHarness(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.fake.Handle("ports.list", func(json.RawMessage) (any, error) {
		once.Do(func() { close(entered) })
		<-release
		return rpc.PortsListResult{Ports: []state.Port{}}, nil
	})

	done := make(chan *mcpserver.ErrorPayload, 1)
	go func() {
		res := h.call("list_ports", map[string]any{})
		payload, ok := mcpserver.DecodeError(res)
		if !ok {
			done <- nil
			return
		}
		done <- &payload
	}()

	<-entered
	h.fake.Stop() // the daemon dies with the call in flight
	close(release)

	select {
	case payload := <-done:
		if payload == nil {
			t.Fatal("a call cut off by a dying daemon did not report an error")
		}
		if payload.Error.Code != mcpserver.CodeDaemonUnavailable {
			t.Fatalf("code = %q, want %q", payload.Error.Code, mcpserver.CodeDaemonUnavailable)
		}
		if !strings.Contains(payload.Error.Hint, "daemon.log") {
			t.Errorf("the hint should name the daemon log, got %q", payload.Error.Hint)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call never returned after the daemon died")
	}

	// A call made while there is no daemon is the same domain error, not a
	// protocol failure.
	payload, ok := mcpserver.DecodeError(h.call("list_ports", map[string]any{}))
	if !ok || payload.Error.Code != mcpserver.CodeDaemonUnavailable {
		t.Fatalf("a call with no daemon should be %s, got %+v", mcpserver.CodeDaemonUnavailable, payload)
	}

	// And the server reconnects on its own once the daemon comes back.
	h.fake.ResetHandlers()
	if err := h.fake.Restart(); err != nil {
		t.Fatalf("restarting the fake daemon: %v", err)
	}
	waitFor(t, 10*time.Second, "the server to reconnect", h.server.Daemon().Connected)

	out := structured[struct {
		Ports []state.Port `json:"ports"`
	}](t, h.call("list_ports", map[string]any{}))
	if len(out.Ports) == 0 {
		t.Fatal("list_ports returned nothing after the reconnect")
	}
	if h.server.Daemon().Reconnects() != 1 {
		t.Errorf("reconnects = %d, want 1", h.server.Daemon().Reconnects())
	}
}

// TestSubscriptionIsReissuedOnReconnect covers the other half of the reconnect
// contract: a state.subscribe survives the daemon dying, and the resume hook
// fires with the fresh snapshot so a resource layer can re-announce its URIs.
func TestSubscriptionIsReissuedOnReconnect(t *testing.T) {
	h := newHarness(t)

	var mu sync.Mutex
	var resumes int
	deltas := make(chan state.Delta, 4)
	err := h.server.Daemon().Subscribe(context.Background(), client.SubscribeOptions{},
		func(d state.Delta) { deltas <- d },
		func(state.Snapshot) {
			mu.Lock()
			resumes++
			mu.Unlock()
		})
	if err != nil {
		t.Fatalf("subscribing: %v", err)
	}
	waitFor(t, 5*time.Second, "the fake to see a subscriber", func() bool {
		return h.fake.Subscribers() == 1
	})

	h.fake.Push(state.Delta{})
	select {
	case <-deltas:
	case <-time.After(5 * time.Second):
		t.Fatal("no delta arrived on the first connection")
	}

	h.fake.Stop()
	if err := h.fake.Restart(); err != nil {
		t.Fatalf("restarting the fake daemon: %v", err)
	}
	waitFor(t, 10*time.Second, "the subscription to be re-issued", func() bool {
		return h.fake.Subscribers() == 1
	})

	mu.Lock()
	got := resumes
	mu.Unlock()
	if got < 2 {
		t.Errorf("the resume hook fired %d times, want it once per connection", got)
	}

	h.fake.Push(state.Delta{})
	select {
	case <-deltas:
	case <-time.After(5 * time.Second):
		t.Fatal("no delta arrived after the reconnect: the subscription was not re-issued")
	}
}

// TestNoDaemonAtStartupIsFatal: unlike the CLI's read commands, which fall back
// to a direct scan (contract §20), the MCP server has nothing to serve without
// a daemon and must fail loudly instead of registering broken tools.
func TestNoDaemonAtStartupIsFatal(t *testing.T) {
	_, err := mcpserver.New(context.Background(), mcpserver.Options{
		Version: "test",
		DaemonOptions: mcpserver.DaemonOptions{
			Socket:      fakedaemon.TempAddr(),
			NoAutostart: true,
			Timeout:     2 * time.Second,
		},
	})
	if err == nil {
		t.Fatal("starting the MCP server with no daemon should fail")
	}
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
