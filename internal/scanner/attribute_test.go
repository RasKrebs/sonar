package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/raskrebs/sonar/internal/store"
)

// openStore gives a test its own database file.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sonar.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// loopWith builds a loop over a fixed scan result, backed by st.
func loopWith(st Store, rows ...ports.ListeningPort) *Loop {
	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			out := make([]ports.ListeningPort, len(rows))
			copy(out, rows)
			return out, nil
		},
	})
	return l
}

func TestScanAppliesRenamesAndPins(t *testing.T) {
	st := openStore(t)
	if err := st.SetRename("port:8123", "storefront"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPin("port:8123", "shop"); err != nil {
		t.Fatal(err)
	}

	l := loopWith(st, ports.ListeningPort{
		Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server",
	})

	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Ports) != 1 {
		t.Fatalf("snapshot has %d ports, want 1", len(snap.Ports))
	}
	p := snap.Ports[0]
	if p.DisplayName != "storefront" {
		t.Errorf("display_name = %q, want storefront", p.DisplayName)
	}
	if p.Name == nil || *p.Name != "storefront" {
		t.Errorf("name = %v, want storefront", p.Name)
	}
	if deref(p.Group) != "shop" {
		t.Errorf("group = %q, want shop", deref(p.Group))
	}
	if p.GroupSource == nil || *p.GroupSource != state.SourceManual {
		t.Errorf("group_source = %v, want manual", p.GroupSource)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "shop" {
		t.Fatalf("groups = %+v, want one group named shop", snap.Groups)
	}
	if len(snap.Groups[0].Members) != 1 || snap.Groups[0].Members[0] != 8123 {
		t.Errorf("group members = %v, want [8123]", snap.Groups[0].Members)
	}
}

func TestScanWithoutAStoreLeavesNamesAlone(t *testing.T) {
	l := loopWith(nil, ports.ListeningPort{
		Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server",
	})
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Ports[0].Name != nil {
		t.Errorf("name = %v, want null with no store", *snap.Ports[0].Name)
	}
	if snap.Ports[0].DisplayName == "" {
		t.Error("display_name is empty; the derived name should still be there")
	}
}

func TestScanRecordsHistoryFromTheDiff(t *testing.T) {
	st := openStore(t)

	up := ports.ListeningPort{Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server"}
	var rows []ports.ListeningPort
	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			out := make([]ports.ListeningPort, len(rows))
			copy(out, rows)
			return out, nil
		},
	})

	rows = []ports.ListeningPort{up}
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	// Same key, new PID: the diff calls that a restart, not an up/down pair.
	restarted := up
	restarted.PID = 43
	rows = []ports.ListeningPort{restarted}
	l.Invalidate()
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	rows = nil
	l.Invalidate()
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}

	events, err := st.Query(nil, time.Time{}, 0)
	if err != nil {
		t.Fatalf("querying history: %v", err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []string{store.EventPortDown, store.EventPortRestarted, store.EventPortUp}
	if len(kinds) != len(want) {
		t.Fatalf("history kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("history kinds = %v, want %v (newest first)", kinds, want)
		}
	}
	if events[0].Port != 8123 || events[0].Command == "" {
		t.Errorf("history row = %+v, want port 8123 with its command", events[0])
	}
}

func TestSeedsTheIndexFromKnownRoots(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: seeded\n")
	if err := st.AddRoot(dir); err != nil {
		t.Fatal(err)
	}

	l := loopWith(st)
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "seeded" {
		t.Fatalf("groups = %+v, want the seeded config's group", snap.Groups)
	}
	if snap.Groups[0].Status != "stopped" {
		t.Errorf("status = %q, want stopped for a project with nothing running", snap.Groups[0].Status)
	}
}

func TestRemembersNewlySeenRoots(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: observed\n")

	l := loopWith(st, ports.ListeningPort{
		Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server", Cwd: dir,
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	roots, err := st.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != resolved(t, dir) {
		t.Fatalf("known roots = %v, want [%s]", roots, resolved(t, dir))
	}
}

func TestInvalidateForcesTheNextReadToRescan(t *testing.T) {
	scans := 0
	l := New(Options{
		HostStats:     fixedHost,
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			return nil, nil
		},
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("scans = %d, want the second read served from the cache", scans)
	}
	l.Invalidate()
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatalf("scans = %d, want a rescan after Invalidate", scans)
	}
}

func TestSetStoreInstallsOneAfterConstruction(t *testing.T) {
	st := openStore(t)
	if err := st.SetRename("port:8123", "late"); err != nil {
		t.Fatal(err)
	}
	l := loopWith(nil, ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"})

	l.SetStore(st)
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Ports[0].DisplayName != "late" {
		t.Errorf("display_name = %q, want late", snap.Ports[0].DisplayName)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".sonar.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs)
}
