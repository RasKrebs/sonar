package runs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	want := filepath.Join("/tmp/fakehome", ".config", "sonar", "runs.json")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reg := Load()
	if reg == nil || reg.Runs == nil {
		t.Fatal("Load() of missing file should yield empty registry, not nil")
	}
	if len(reg.Runs) != 0 {
		t.Errorf("missing file should yield empty registry, got %+v", reg.Runs)
	}
}

func TestAddSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Use our own (live) pid so prune doesn't drop it.
	self := os.Getpid()
	if err := Add(Entry{PID: self, Tag: "demo", ID: "abc123", Cmd: "python -m http.server"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	reg := Load()
	e, ok := reg.LookupByPID(self)
	if !ok {
		t.Fatalf("entry for pid %d not found after Add", self)
	}
	if e.Tag != "demo" || e.ID != "abc123" {
		t.Errorf("round-trip mismatch: got %+v", e)
	}
	if e.StartedAt == "" {
		t.Error("Add should stamp StartedAt")
	}
}

func TestRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	self := os.Getpid()
	if err := Add(Entry{PID: self, Tag: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(self); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := Load().LookupByPID(self); ok {
		t.Error("entry still present after Remove")
	}
	// Removing a missing pid is a no-op.
	if err := Remove(999999); err != nil {
		t.Errorf("Remove of missing pid should be no-op, got %v", err)
	}
}

func TestLoadPrunesDeadPIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A live pid (ours) and a dead pid (a process we start then reap).
	self := os.Getpid()
	dead := spawnAndReap(t)

	if err := Add(Entry{PID: self, Tag: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := Add(Entry{PID: dead, Tag: "dead"}); err != nil {
		t.Fatal(err)
	}

	reg := Load()
	if _, ok := reg.LookupByPID(self); !ok {
		t.Errorf("live pid %d was pruned", self)
	}
	if _, ok := reg.LookupByPID(dead); ok {
		t.Errorf("dead pid %d should have been pruned", dead)
	}

	// Prune should have persisted: re-reading the file shows only the live pid.
	reg2 := load()
	if _, ok := reg2.Runs[dead]; ok {
		t.Error("dead pid not removed from disk after prune")
	}
}

func TestLoadMalformedFileRecovers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "sonar")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := Load()
	if reg == nil || reg.Runs == nil || len(reg.Runs) != 0 {
		t.Errorf("malformed file should recover to empty registry, got %+v", reg)
	}
}

// spawnAndReap starts a trivial short-lived process, waits for it to exit, and
// returns its (now-dead) pid.
func spawnAndReap(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Start(); err != nil {
		// Fall back to a portable no-op if `true` is unavailable.
		c = exec.Command("sh", "-c", "exit 0")
		if err := c.Start(); err != nil {
			t.Skipf("cannot spawn helper process: %v", err)
		}
	}
	pid := c.Process.Pid
	_ = c.Wait()
	return pid
}
