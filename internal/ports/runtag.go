package ports

import "github.com/raskrebs/sonar/internal/runs"

// runTagger attributes a listening PID to a tagged `sonar run` by walking the
// PPID ancestry: a dev server is typically a descendant of the `sonar run`
// process (e.g. `sonar run -- npm` -> npm -> vite -> esbuild). If any ancestor
// PID is in the runs registry, the listener inherits that entry's tag/id.
//
// Results are memoized per PID across the whole listener set, so an ancestry
// chain shared by sibling listeners is only walked once.
type runTagger struct {
	reg   *runs.Registry
	cache map[int]tagResult // pid -> resolved tag (including negative results)
}

type tagResult struct {
	tag       string
	id        string
	rootPID   int    // pid of the registry entry the listener descends from
	startedAt string // RFC3339, recorded when the run was registered
	ok        bool
}

// newRunTagger loads (and prunes) the runs registry once. When the registry is
// empty, lookups short-circuit so there is no per-PID work in the common case.
func newRunTagger() *runTagger {
	return &runTagger{
		reg:   runs.Load(),
		cache: make(map[int]tagResult),
	}
}

// lookup resolves the tag for a listener PID by walking up its ancestry using
// the already-collected pidInfo map (pid -> {ppid, cmd}). No syscalls/exec are
// performed here: the parent map is reused, and per-PID results are cached.
func (t *runTagger) lookup(pid int, pidInfo map[int]pidEntry) tagResult {
	if t == nil || len(t.reg.Runs) == 0 {
		return tagResult{}
	}
	return t.walk(pid, pidInfo, 0)
}

// walk recurses up the PPID chain, caching every PID it visits (positive and
// negative) so shared ancestry is only resolved once. A depth bound guards
// against pathological / cyclic process tables.
func (t *runTagger) walk(pid int, pidInfo map[int]pidEntry, depth int) tagResult {
	if pid <= 1 || depth > 64 {
		return tagResult{}
	}
	if cached, ok := t.cache[pid]; ok {
		return cached
	}

	// Direct hit: this PID is itself a tagged run.
	if e, ok := t.reg.LookupByPID(pid); ok {
		res := tagResult{tag: e.Tag, id: e.ID, rootPID: e.PID, startedAt: e.StartedAt, ok: true}
		t.cache[pid] = res
		return res
	}

	// Otherwise climb to the parent.
	info, ok := pidInfo[pid]
	if !ok || info.ppid <= 1 || info.ppid == pid {
		t.cache[pid] = tagResult{}
		return tagResult{}
	}
	res := t.walk(info.ppid, pidInfo, depth+1)
	t.cache[pid] = res
	return res
}
