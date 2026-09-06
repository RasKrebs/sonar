package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/raskrebs/sonar/internal/store"
)

// storeHarness is a harness with a temp database already open and one port
// listening, which is what every write path needs.
func storeHarness(t *testing.T, ctx context.Context, rows ...ports.ListeningPort) (*testHarness, *store.Store) {
	t.Helper()
	// The temp directory is claimed first so its removal is the last cleanup
	// to run: the harness holds the database inside it and has to close it
	// before anything deletes the file.
	db := filepath.Join(t.TempDir(), "sonar.db")
	h := newHarness(t, ctx)
	st := h.withStore(db)
	if len(rows) == 0 {
		rows = []ports.ListeningPort{{
			Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server",
		}}
	}
	h.setRows(rows...)
	return h, st
}

func intp(v int) *int            { return &v }
func strp(v string) *string      { return &v }
func nameOf(p state.Port) string { return p.DisplayName }

func TestRenameShowsUpInTheNextDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, st := storeHarness(t, ctx)
	c := h.dial(ctx)

	first := c.subscribeAndSettle(rpc.StateSubscribeParams{})
	if len(first.Ports.Added) != 1 || nameOf(first.Ports.Added[0]) == "storefront" {
		t.Fatalf("first delta = %+v, want the unrenamed port", first.Ports)
	}

	// The rescan the write triggers publishes before the reply is written
	// (contract §18, see republish), so the delta and the response have to be
	// collected together.
	res, delta := c.renameCollectingDelta(t, rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123)}, Name: strp("storefront"),
	})
	if !res.OK || res.Key != "port:8123" {
		t.Errorf("result = %+v, want ok with key port:8123", res)
	}
	if len(res.Affected) != 1 || res.Affected[0] != "8123:" {
		t.Errorf("affected = %v, want the port key", res.Affected)
	}
	if got, _, err := st.GetRename("port:8123"); err != nil || got != "storefront" {
		t.Errorf("stored rename = %q (%v), want storefront", got, err)
	}

	if len(delta.Ports.Updated) != 1 || delta.Ports.Updated[0].DisplayName != "storefront" {
		t.Fatalf("delta = %+v, want the renamed port", delta.Ports)
	}
	if delta.Ports.Updated[0].Name == nil || *delta.Ports.Updated[0].Name != "storefront" {
		t.Errorf("name = %v, want storefront alongside display_name", delta.Ports.Updated[0].Name)
	}
}

func TestRenameWithANullNameClearsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, st := storeHarness(t, ctx)
	c := h.dial(ctx)

	if e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123)}, Name: strp("storefront"),
	}, nil); e != nil {
		t.Fatalf("ports.rename: %v", e)
	}
	var res rpc.PortsRenameResult
	if e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123)},
	}, &res); e != nil {
		t.Fatalf("clearing the rename: %v", e)
	}
	if res.Name != nil {
		t.Errorf("name = %v, want null after a clear", *res.Name)
	}
	if _, ok, _ := st.GetRename("port:8123"); ok {
		t.Error("the rename is still in the database after a clear")
	}
}

func TestAssignPinsTheGroupAsManual(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := storeHarness(t, ctx)
	c := h.dial(ctx)

	var res rpc.GroupsAssignResult
	if e := c.call("groups.assign", rpc.GroupsAssignParams{
		Selector: rpc.Selector{Port: intp(8123)}, Group: strp("shop"),
	}, &res); e != nil {
		t.Fatalf("groups.assign: %v", e)
	}
	if !res.OK || res.Key != "port:8123" {
		t.Errorf("result = %+v, want ok with key port:8123", res)
	}

	var snap state.Snapshot
	if e := c.call("state.snapshot", rpc.StateSnapshotParams{}, &snap); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	p := snap.Ports[0]
	if p.Group == nil || *p.Group != "shop" {
		t.Fatalf("group = %v, want shop", p.Group)
	}
	if p.GroupSource == nil || *p.GroupSource != state.SourceManual {
		t.Errorf("group_source = %v, want manual", p.GroupSource)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "shop" {
		t.Errorf("groups = %+v, want one group named shop", snap.Groups)
	}
}

func TestRenameRejectsAnUnknownPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := storeHarness(t, ctx)
	c := h.dial(ctx)

	e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(9999)}, Name: strp("nope"),
	}, nil)
	if e == nil {
		t.Fatal("renaming a port nothing listens on succeeded")
	}
	if e.Code != rpc.CodeNotFound || e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}
}

func TestRenameNeedsASelector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := storeHarness(t, ctx)
	c := h.dial(ctx)

	e := c.call("ports.rename", rpc.PortsRenameParams{Name: strp("nope")}, nil)
	if e == nil || e.Data.Code != "invalid_selector" {
		t.Errorf("error = %+v, want invalid_selector", e)
	}
}

func TestRenameIsAmbiguousWhenTwoProjectsShareAPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := storeHarness(t, ctx,
		ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "node", Cwd: t.TempDir()},
		ports.ListeningPort{Port: 8123, BindAddress: "::1", PID: 43, Process: "node", Cwd: t.TempDir()},
	)
	c := h.dial(ctx)

	e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123)}, Name: strp("web"),
	}, nil)
	if e == nil {
		t.Fatal("renaming a multi-bind port with no bind_address succeeded")
	}
	if e.Code != rpc.CodeAmbiguous || e.Data.Code != "ambiguous" {
		t.Fatalf("error = %+v, want ambiguous", e)
	}

	// With the address it resolves.
	if e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123), BindAddress: strp("::1")}, Name: strp("web"),
	}, nil); e != nil {
		t.Fatalf("ports.rename with a bind_address: %v", e)
	}
}

func TestMultiBindOfOneServiceIsNotAmbiguous(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Same process, same key: 0.0.0.0 and :: are one service.
	h, _ := storeHarness(t, ctx,
		ports.ListeningPort{Port: 8123, BindAddress: "0.0.0.0", PID: 42, Process: "python3"},
		ports.ListeningPort{Port: 8123, BindAddress: "::", PID: 42, Process: "python3"},
	)
	c := h.dial(ctx)

	var res rpc.PortsRenameResult
	if e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123)}, Name: strp("web"),
	}, &res); e != nil {
		t.Fatalf("ports.rename: %v", e)
	}
	if res.Key != "port:8123" {
		t.Errorf("key = %q, want port:8123", res.Key)
	}
}

func TestHistoryFiltersByPortAndLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, st := storeHarness(t, ctx)
	c := h.dial(ctx)

	rows := []store.HistoryEvent{
		{Kind: store.EventPortUp, Port: 8123, PID: 42, DisplayName: "web", Group: "shop"},
		{Kind: store.EventPortDown, Port: 8123, PID: 42, DisplayName: "web", Group: "shop"},
		{Kind: store.EventPortUp, Port: 3000, PID: 43, DisplayName: "api"},
	}
	if err := st.AppendBatch(rows); err != nil {
		t.Fatal(err)
	}

	var all rpc.PortsHistoryResult
	if e := c.call("ports.history", rpc.PortsHistoryParams{}, &all); e != nil {
		t.Fatalf("ports.history: %v", e)
	}
	if len(all.Events) != 3 {
		t.Fatalf("history has %d events, want 3", len(all.Events))
	}

	var one rpc.PortsHistoryResult
	if e := c.call("ports.history", rpc.PortsHistoryParams{Port: intp(8123)}, &one); e != nil {
		t.Fatalf("ports.history: %v", e)
	}
	if len(one.Events) != 2 {
		t.Fatalf("history for 8123 has %d events, want 2", len(one.Events))
	}
	for _, e := range one.Events {
		if e.Port != 8123 {
			t.Errorf("event for port %d leaked into the 8123 filter", e.Port)
		}
	}

	var limited rpc.PortsHistoryResult
	if e := c.call("ports.history", rpc.PortsHistoryParams{Limit: 1}, &limited); e != nil {
		t.Fatalf("ports.history: %v", e)
	}
	if len(limited.Events) != 1 {
		t.Errorf("limit 1 returned %d events", len(limited.Events))
	}

	var recent rpc.PortsHistoryResult
	if e := c.call("ports.history", rpc.PortsHistoryParams{Since: strp("1h")}, &recent); e != nil {
		t.Fatalf("ports.history --since 1h: %v", e)
	}
	if len(recent.Events) != 3 {
		t.Errorf("since 1h returned %d events, want all 3", len(recent.Events))
	}

	var old rpc.PortsHistoryResult
	if e := c.call("ports.history", rpc.PortsHistoryParams{Since: strp("2999-01-01T00:00:00Z")}, &old); e != nil {
		t.Fatalf("ports.history --since <future>: %v", e)
	}
	if len(old.Events) != 0 {
		t.Errorf("a future since returned %d events, want none", len(old.Events))
	}

	if e := c.call("ports.history", rpc.PortsHistoryParams{Since: strp("yesterday")}, nil); e == nil ||
		e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params for an unparseable since", e)
	}
}

func TestConfigRoundTripThroughTheDaemon(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var path rpc.ConfigPathResult
	if e := c.call("config.path", rpc.Empty{}, &path); e != nil {
		t.Fatalf("config.path: %v", e)
	}
	if filepath.Dir(filepath.Dir(path.Path)) != filepath.Join(home, ".config") {
		t.Errorf("path = %q, want it under the temp home", path.Path)
	}

	var set rpc.ConfigSetResult
	if e := c.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"list": map[string]any{"sort": "pid"}},
	}, &set); e != nil {
		t.Fatalf("config.set: %v", e)
	}
	if !set.OK {
		t.Error("config.set did not report ok")
	}
	if _, err := os.Stat(path.Path); err != nil {
		t.Fatalf("config.set wrote no file: %v", err)
	}

	var got rpc.ConfigGetResult
	if e := c.call("config.get", rpc.Empty{}, &got); e != nil {
		t.Fatalf("config.get: %v", e)
	}
	if got.Config["list"].(map[string]any)["sort"] != "pid" {
		t.Errorf("config = %v, want list.sort pid", got.Config)
	}

	// A second write keeps the previous file next to it.
	if e := c.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"list": map[string]any{"sort": "port"}},
	}, nil); e != nil {
		t.Fatalf("config.set: %v", e)
	}
	if _, err := os.Stat(path.Path + ".bak"); err != nil {
		t.Errorf("no backup beside the config: %v", err)
	}

	if e := c.call("config.set", rpc.ConfigSetParams{
		Patch: map[string]any{"list": map[string]any{"sort": "sideways"}},
	}, nil); e == nil || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params for a rejected value", e)
	}
}

func TestWritesWithoutADatabaseFail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	h.setRows(ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"})
	c := h.dial(ctx)

	e := c.call("ports.rename", rpc.PortsRenameParams{
		Selector: rpc.Selector{Port: intp(8123)}, Name: strp("web"),
	}, nil)
	if e == nil || e.Code != rpc.CodeInternal {
		t.Errorf("error = %+v, want an internal error naming the missing database", e)
	}
}

// renameCollectingDelta sends ports.rename and returns both its result and the
// state.delta that carried the rename, which the daemon publishes before it
// writes the reply (contract §18, see republish).
//
// It keeps the last delta that actually moved a port, not the last delta of any
// kind. `hosts` carries this machine's own cpu, load and memory, so a
// subscribed connection gets a delta on ticks where no port changed at all, and
// one of those can land between the rename's delta and the reply. Asserting on
// whichever delta happened to arrive last made this test read a load-only
// notification and call the rename missing; the last port-moving delta before
// the reply is the thing §18 actually promises, and it also catches a later
// delta putting the old name back.
func (c *testClient) renameCollectingDelta(t *testing.T, p rpc.PortsRenameParams) (rpc.PortsRenameResult, state.Delta) {
	t.Helper()
	id := c.send("ports.rename", p)
	var (
		res   rpc.PortsRenameResult
		delta state.Delta
		seen  bool
	)
	for i := 0; i < 50; i++ {
		m := c.read()
		switch {
		case m.IsNotification() && m.Method == rpc.MethodStateDelta:
			var d state.Delta
			if err := json.Unmarshal(m.Params, &d); err != nil {
				t.Fatalf("decoding state.delta: %v", err)
			}
			if len(d.Ports.Added)+len(d.Ports.Updated)+len(d.Ports.Removed) == 0 {
				continue
			}
			delta, seen = d, true
		case m.IsResponse() && string(m.ID) == id:
			if m.Error != nil {
				t.Fatalf("ports.rename: %v", m.Error)
			}
			if err := json.Unmarshal(m.Result, &res); err != nil {
				t.Fatalf("decoding the rename result: %v", err)
			}
			if seen {
				return res, delta
			}
		}
	}
	t.Fatal("no state.delta carrying a port change followed the rename")
	return res, delta
}
