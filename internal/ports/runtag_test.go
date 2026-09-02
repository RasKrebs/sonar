package ports

import (
	"os"
	"testing"

	"github.com/raskrebs/sonar/internal/runs"
)

// setupRegistry points HOME at a temp dir and seeds the runs registry with the
// given entries, returning nothing (Load reads from HOME).
func setupRegistry(t *testing.T, entries ...runs.Entry) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, e := range entries {
		if err := runs.Add(e); err != nil {
			t.Fatalf("seed registry: %v", err)
		}
	}
}

func TestRunTagger_AncestryWalk(t *testing.T) {
	// `sonar run` pid 100 (tag "demo") -> npm 200 -> vite 300 -> esbuild 400.
	// The listener is esbuild (400); it should inherit the tag via ancestry.
	// Use our own pid as the tagged-run pid so the registry prune keeps it alive.
	runPID := os.Getpid()
	setupRegistry(t, runs.Entry{PID: runPID, Tag: "demo", ID: "chg-1", Cmd: "npm run dev"})

	pidInfo := map[int]pidEntry{
		runPID: {ppid: 1, cmd: "sonar run"},
		200:    {ppid: runPID, cmd: "npm run dev"},
		300:    {ppid: 200, cmd: "vite"},
		400:    {ppid: 300, cmd: "esbuild"},
		// An unrelated process tree, should NOT be tagged.
		500: {ppid: 1, cmd: "redis-server"},
	}

	tagger := newRunTagger()

	res := tagger.lookup(400, pidInfo)
	if !res.ok || res.tag != "demo" || res.id != "chg-1" {
		t.Fatalf("descendant listener: got %+v, want (demo,chg-1,true)", res)
	}
	if res.rootPID != runPID {
		t.Fatalf("descendant listener rootPID = %d, want %d", res.rootPID, runPID)
	}

	// Direct hit on the run pid itself.
	if res := tagger.lookup(runPID, pidInfo); !res.ok || res.tag != "demo" {
		t.Errorf("direct pid: got %+v, want (demo,true)", res)
	}

	// Unrelated tree is untagged.
	if res := tagger.lookup(500, pidInfo); res.ok {
		t.Error("unrelated process should not be tagged")
	}
}

func TestRunTagger_EmptyRegistryShortCircuits(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no runs.json
	tagger := newRunTagger()
	pidInfo := map[int]pidEntry{42: {ppid: 1, cmd: "x"}}
	if res := tagger.lookup(42, pidInfo); res.ok {
		t.Error("empty registry should yield no tag")
	}
}

func TestRunTagger_CachesNegativeAndPositive(t *testing.T) {
	runPID := os.Getpid()
	setupRegistry(t, runs.Entry{PID: runPID, Tag: "t"})
	tagger := newRunTagger()
	pidInfo := map[int]pidEntry{
		runPID: {ppid: 1, cmd: "sonar run"},
		10:     {ppid: runPID, cmd: "child"},
		20:     {ppid: 1, cmd: "other"},
	}
	// Resolve a descendant (positive) and an unrelated (negative).
	tagger.lookup(10, pidInfo)
	tagger.lookup(20, pidInfo)
	if res, ok := tagger.cache[10]; !ok || !res.ok {
		t.Error("positive result not cached")
	}
	if res, ok := tagger.cache[20]; !ok || res.ok {
		t.Error("negative result not cached")
	}
}

func TestRunTagger_CycleGuard(t *testing.T) {
	runPID := os.Getpid()
	setupRegistry(t, runs.Entry{PID: runPID, Tag: "t"})
	tagger := newRunTagger()
	// Pathological cycle: 10 <-> 11, neither in registry. Must terminate.
	pidInfo := map[int]pidEntry{
		10: {ppid: 11, cmd: "a"},
		11: {ppid: 10, cmd: "b"},
	}
	if res := tagger.lookup(10, pidInfo); res.ok {
		t.Error("cycle should not produce a tag")
	}
}
