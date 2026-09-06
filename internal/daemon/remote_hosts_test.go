package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// remoteRows is what a connected bridge contributes: rows already tagged with
// the host name, plus that host's own row in the hosts collection.
func remoteRows(host string, port int) state.Rows {
	return state.Rows{
		Ports: []state.Port{{
			Port: port, BindAddress: "127.0.0.1", PID: 4242,
			Process: "node", DisplayName: "node", Type: state.TypeUser,
			ExposedURLs: []string{},
		}},
		Groups: []state.Group{{Name: "api", Status: "running", Members: []int{port}}},
		Hosts:  []state.Host{{Name: state.LocalhostName, Status: state.HostConnected}},
	}.Tag(host).Normalize()
}

// TestSubscribeDefaultsToLocalhost is the backward-compatibility guarantee: a
// client that never learned about `hosts` reads exactly the stream it always
// read, remote hosts registered or not.
func TestSubscribeDefaultsToLocalhost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.loop.SetRemote(func() state.Rows { return remoteRows("hetzner", 3000) })
	h.setRows(ports.ListeningPort{Port: 8080, BindAddress: "127.0.0.1", PID: 1, Process: "go"})

	c := h.dial(ctx)
	var snap state.Snapshot
	if err := c.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}
	for _, p := range snap.Ports {
		if p.Host != state.LocalhostName {
			t.Errorf("snapshot carries a %s row without being asked: %+v", p.Host, p)
		}
	}
	for _, host := range snap.Hosts {
		if host.Name != state.LocalhostName {
			t.Errorf("hosts = %+v, want only localhost", snap.Hosts)
		}
	}
}

// TestSubscribeStarSeesEveryHost is the app's "all hosts" view.
func TestSubscribeStarSeesEveryHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.loop.SetRemote(func() state.Rows { return remoteRows("hetzner", 3000) })

	c := h.dial(ctx)
	var snap state.Snapshot
	if err := c.call("state.subscribe", rpc.StateSubscribeParams{Hosts: []string{"*"}}, &snap); err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	var found bool
	for _, p := range snap.Ports {
		if p.Host == "hetzner" {
			found = true
			if want := "hetzner/3000:127.0.0.1"; p.Key() != want {
				t.Errorf("remote key = %q, want %q", p.Key(), want)
			}
		}
	}
	if !found {
		t.Errorf(`ports = %+v, want the hetzner row under hosts: ["*"]`, snap.Ports)
	}
	if len(snap.Hosts) != 2 {
		t.Errorf("hosts = %+v, want localhost and hetzner", snap.Hosts)
	}
}

// TestSnapshotHostsFilter checks the same option on the unary read.
func TestSnapshotHostsFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.loop.SetRemote(func() state.Rows { return remoteRows("hetzner", 3000) })
	h.setRows(ports.ListeningPort{Port: 8080, BindAddress: "127.0.0.1", PID: 1, Process: "go"})

	c := h.dial(ctx)
	var only state.Snapshot
	if err := c.call("state.snapshot", rpc.StateSnapshotParams{Hosts: []string{"hetzner"}}, &only); err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	if len(only.Ports) != 1 || only.Ports[0].Host != "hetzner" {
		t.Errorf("ports = %+v, want only hetzner's", only.Ports)
	}
	if len(only.Hosts) != 1 || only.Hosts[0].Name != "hetzner" {
		t.Errorf("hosts = %+v, want only hetzner's row", only.Hosts)
	}
}

// TestRemoteChangePublishesADelta is the multiplexer end to end inside the
// daemon: a bridge reporting a new remote port reaches a subscriber that asked
// for that host, without a local scan having found anything.
func TestRemoteChangePublishesADelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)

	var rows state.Rows
	h.loop.SetRemote(func() state.Rows { return rows })
	h.setRows(ports.ListeningPort{Port: 8080, BindAddress: "127.0.0.1", PID: 1, Process: "go"})

	c := h.dial(ctx)
	var snap state.Snapshot
	if err := c.call("state.subscribe", rpc.StateSubscribeParams{Hosts: []string{"*"}}, &snap); err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	rows = remoteRows("hetzner", 3000)
	h.loop.RemoteChanged()

	for {
		msg := c.read()
		if !msg.IsNotification() || msg.Method != rpc.MethodStateDelta {
			continue
		}
		var d state.Delta
		if err := json.Unmarshal(msg.Params, &d); err != nil {
			t.Fatal(err)
		}
		for _, p := range d.Ports.Added {
			if p.Host == "hetzner" && p.Port == 3000 {
				return
			}
		}
	}
}

// TestRemoteRowsAreInvisibleToALocalSubscriber is the same transition seen by
// the default subscriber: nothing at all, because it asked about localhost.
func TestRemoteRowsAreInvisibleToALocalSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)

	var rows state.Rows
	h.loop.SetRemote(func() state.Rows { return rows })
	h.setRows(ports.ListeningPort{Port: 8080, BindAddress: "127.0.0.1", PID: 1, Process: "go"})

	c := h.dial(ctx)
	var snap state.Snapshot
	if err := c.call("state.subscribe", rpc.StateSubscribeParams{}, &snap); err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	before := h.srv.loop.Status().Seq
	rows = remoteRows("hetzner", 3000)
	h.loop.RemoteChanged()
	if after := h.srv.loop.Status().Seq; after == before {
		t.Fatal("the remote change did not publish at all")
	}

	// The delta the remote change produced is empty for this subscriber, so
	// the only thing it can be sent is a later local one. Prove that by asking
	// a question and checking nothing came first.
	var res rpc.DaemonStatusResult
	if err := c.call("daemon.status", rpc.Empty{}, &res); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
}

// TestEventsAreFilteredByHost: a subscriber that did not ask for a host must
// not be woken by that host's port_up.
func TestEventsAreFilteredByHost(t *testing.T) {
	local := marshalEvents([]state.Event{{Kind: "port_up", Host: "hetzner"}},
		scanner.Include{}, state.LocalOnly())
	if len(local) != 0 {
		t.Errorf("a localhost subscriber got %d remote events", len(local))
	}
	all := marshalEvents([]state.Event{{Kind: "port_up", Host: "hetzner"}},
		scanner.Include{}, state.AllHosts())
	if len(all) != 1 {
		t.Errorf(`a "*" subscriber got %d events, want 1`, len(all))
	}
	untagged := marshalEvents([]state.Event{{Kind: "port_up"}},
		scanner.Include{}, state.LocalOnly())
	if len(untagged) != 1 {
		t.Errorf("an untagged event is a local one and should be delivered")
	}
}
