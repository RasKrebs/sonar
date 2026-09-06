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

// withStore opens the daemon's database at path the way Serve does. Closing it is the harness's
// job (see testHarness.shutdown): the store must outlive the scan loop that
// writes history into it, and a cleanup registered here would run first.
func (h *testHarness) withStore(path string) *store.Store {
	h.t.Helper()
	h.srv.opts.DBPath = path
	h.srv.openStore()
	if h.srv.runtime.Store == nil {
		h.t.Fatalf("no store opened at %s", path)
	}
	return h.srv.runtime.Store
}

func TestStatusReportsTheDatabasePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The database's directory is claimed before the harness exists, so the
	// harness stops before the directory is removed.
	path := filepath.Join(t.TempDir(), "sonar.db")
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var before rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &before); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	if before.DBPath != "" {
		t.Errorf("db_path = %q before the store is open, want empty", before.DBPath)
	}

	h.withStore(path)

	var after rpc.DaemonStatusResult
	if e := c.call("daemon.status", rpc.Empty{}, &after); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	if after.DBPath != path {
		t.Errorf("db_path = %q, want %q", after.DBPath, path)
	}
	if h.srv.runtime.DB == nil {
		t.Error("Runtime.DB is nil; contract §8 hands extensions the raw handle")
	}
}

func TestCorruptDatabaseIsRecreatedAndAnnounced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sonar.db")
	if err := os.WriteFile(path, []byte("this is not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, ctx)
	c := h.dial(ctx)
	if e := c.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, nil); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	h.withStore(path)

	// A change gives the queued event something to ride out on.
	h.setRows(ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"})
	h.loop.Wake()

	ev := c.waitForEvent(t, "db_reset")
	if ev.Data["moved_to"] == nil {
		t.Errorf("db_reset carries no moved_to: %+v", ev.Data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	moved := false
	for _, e := range entries {
		if len(e.Name()) > len("sonar.db.corrupt-") && e.Name()[:len("sonar.db.corrupt-")] == "sonar.db.corrupt-" {
			moved = true
		}
	}
	if !moved {
		t.Errorf("the unusable database was not moved aside: %v", entries)
	}
}

// waitForEvent reads notifications until a state.event of this kind arrives.
func (c *testClient) waitForEvent(t *testing.T, kind string) state.Event {
	t.Helper()
	for i := 0; i < 50; i++ {
		m := c.read()
		if !m.IsNotification() || m.Method != rpc.MethodStateEvent {
			continue
		}
		var ev state.Event
		if err := json.Unmarshal(m.Params, &ev); err != nil {
			t.Fatalf("decoding state.event: %v", err)
		}
		if ev.Kind == kind {
			return ev
		}
	}
	t.Fatalf("no state.event{kind: %q} arrived", kind)
	return state.Event{}
}
