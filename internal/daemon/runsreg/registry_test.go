package runsreg

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/runs"
	"github.com/raskrebs/sonar/internal/state"
)

// testRegistry is a registry with no process table and no runs.json behind it.
func testRegistry(alive ...int) *Registry {
	live := map[int]bool{}
	for _, pid := range alive {
		live[pid] = true
	}
	r := New()
	r.Mirror = false
	r.Alive = func(pid int) bool { return live[pid] }
	r.Parents = func() map[int]int { return nil }
	return r
}

func TestRegisterListAndUnregister(t *testing.T) {
	r := testRegistry(100, 200)
	base := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)

	r.Register(Record{ID: "b", PID: 200, Group: "g", Name: "api", StartedAt: base.Add(time.Minute)})
	r.Register(Record{ID: "a", PID: 100, Group: "g", Name: "web", StartedAt: base})

	got := r.List()
	if len(got) != 2 {
		t.Fatalf("List = %d runs, want 2", len(got))
	}
	if got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("List is not oldest first: %v", []string{got[0].ID, got[1].ID})
	}

	if !r.Unregister(100) {
		t.Fatal("Unregister(100) reported nothing to remove")
	}
	if r.Unregister(100) {
		t.Fatal("Unregister(100) removed the same run twice")
	}
	if len(r.List()) != 1 {
		t.Fatalf("List after unregister = %v", r.List())
	}
}

func TestRegisterKeepsTheIDOfAReregisteredPID(t *testing.T) {
	r := testRegistry(100)
	r.Register(Record{ID: "keep", PID: 100, Group: "g", Name: "web"})
	rec := r.Register(Record{PID: 100, Group: "g", Name: "web"})
	if rec.ID != "keep" {
		t.Fatalf("id = %q, want keep", rec.ID)
	}
}

func TestPruneDropsDeadRuns(t *testing.T) {
	r := testRegistry(100)
	r.Register(Record{ID: "a", PID: 100, Group: "g", Name: "web"})
	r.Register(Record{ID: "b", PID: 999, Group: "g", Name: "gone"})

	r.Prune()
	got := r.List()
	if len(got) != 1 || got[0].PID != 100 {
		t.Fatalf("Prune left %v, want only pid 100", got)
	}
}

func TestRunAttributesAPortByItsPPIDAncestry(t *testing.T) {
	r := testRegistry(100)
	r.Register(Record{ID: "a", PID: 100, Group: "itest", Name: "web"})
	// sonar start (100) -> npm (200) -> node (300) -> esbuild (400)
	r.Parents = func() map[int]int { return map[int]int{400: 300, 300: 200, 200: 100} }

	group, name, ok := r.Run(state.Port{PID: 400, PPID: 300})
	if !ok || group != "itest" || name != "web" {
		t.Fatalf("Run(descendant) = %q/%q/%v, want itest/web/true", group, name, ok)
	}

	if _, _, ok := r.Run(state.Port{PID: 777, PPID: 1}); ok {
		t.Fatal("an unrelated listener was attributed to a run")
	}
}

func TestRunUsesTheDirectParentBeforeTheProcessTable(t *testing.T) {
	r := testRegistry(100)
	r.Register(Record{ID: "a", PID: 100, Group: "itest", Name: "web"})
	r.Parents = func() map[int]int { t.Fatal("the process table should not be needed"); return nil }

	if group, _, ok := r.Run(state.Port{PID: 200, PPID: 100}); !ok || group != "itest" {
		t.Fatalf("Run(child) = %q/%v", group, ok)
	}
}

func TestRunFallsBackToTheScannersOwnAttribution(t *testing.T) {
	r := testRegistry()
	group, name, ok := r.Run(state.Port{
		PID: 400,
		Run: &state.Run{ID: "x", Group: "from-file", Name: "web", RootPID: 100},
	})
	if !ok || group != "from-file" || name != "web" {
		t.Fatalf("Run = %q/%q/%v", group, name, ok)
	}
}

func TestImportLegacyTakesOverRunsJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	live := os.Getpid()
	if err := runs.Add(runs.Entry{PID: live, Tag: "legacy", ID: "old-1", Cmd: "npm run dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runs.Path()); err != nil {
		t.Fatalf("runs.json was not written: %v", err)
	}

	r := New()
	r.Mirror = false
	if n := r.ImportLegacy(); n != 1 {
		t.Fatalf("ImportLegacy = %d, want 1", n)
	}
	if _, err := os.Stat(runs.Path()); !os.IsNotExist(err) {
		t.Fatalf("runs.json survived the import: %v", err)
	}

	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List = %v", got)
	}
	// A `sonar run --tag legacy` entry carries no group or name of its own, so
	// the tag stands for both (the documented alias).
	if got[0].Group != "legacy" || got[0].Name != "legacy" || got[0].ID != "old-1" {
		t.Fatalf("imported record = %+v", got[0])
	}
}

func TestImportLegacyRewritesTheFileItOwns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	live := os.Getpid()
	if err := runs.Add(runs.Entry{PID: live, Tag: "legacy", ID: "old-1"}); err != nil {
		t.Fatal(err)
	}

	r := New() // mirroring on: the daemon owns the file from here
	if n := r.ImportLegacy(); n != 1 {
		t.Fatalf("ImportLegacy = %d, want 1", n)
	}
	data, err := os.ReadFile(runs.Path())
	if err != nil {
		t.Fatalf("the daemon did not rewrite runs.json: %v", err)
	}
	var file struct {
		Runs map[string]runs.Entry `json:"runs"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	entry, ok := file.Runs[itoa(live)]
	if !ok {
		t.Fatalf("runs.json = %s", data)
	}
	if entry.Group != "legacy" || entry.Name != "legacy" {
		t.Fatalf("mirrored entry = %+v", entry)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
