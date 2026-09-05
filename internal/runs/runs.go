// Package runs implements the tagged-runs registry: a small on-disk JSON file
// recording processes spawned via `sonar run`, keyed by PID. Each entry carries
// a caller-supplied tag (and optional stable id) so that `sonar list` can
// attribute listening ports back to whoever started them.
//
// The registry lives next to sonar's config (e.g. ~/.config/sonar/runs.json).
// Writes are serialized with a sidecar lock file and an atomic rename so that
// multiple concurrent `sonar run` invocations don't clobber each other.
package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Entry is a single tagged run, keyed in the registry by its PID.
//
// Group, Name, Cwd, PPID and PortHint arrived with `sonar start` (step 1A.5).
// They are all omitempty, so a file written by an older `sonar run` still
// loads: Tag then stands for both the group and the name, which is exactly the
// migration `sonar run --tag X` -> `sonar start --group X` promises.
type Entry struct {
	PID       int    `json:"pid"`
	Tag       string `json:"tag"`
	ID        string `json:"id,omitempty"`
	Cmd       string `json:"cmd,omitempty"`
	StartedAt string `json:"startedAt,omitempty"` // RFC3339
	Group     string `json:"group,omitempty"`
	Name      string `json:"name,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	PPID      int    `json:"ppid,omitempty"`
	PortHint  int    `json:"portHint,omitempty"`
}

// GroupOf is the group this run attributes its ports to.
func (e Entry) GroupOf() string {
	if e.Group != "" {
		return e.Group
	}
	return e.Tag
}

// NameOf is the service name this run attributes its ports to.
func (e Entry) NameOf() string {
	if e.Name != "" {
		return e.Name
	}
	return e.Tag
}

// Registry is the in-memory view of the on-disk runs file: pid -> entry.
type Registry struct {
	Runs map[int]Entry `json:"runs"`
}

// Path returns the absolute path to the runs registry file. It mirrors the
// config package's layout (~/.config/sonar) without importing it, to avoid a
// dependency cycle and to keep this package self-contained.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "sonar", "runs.json")
}

// lockPath returns the sidecar lock file path for the registry.
func lockPath() string {
	return Path() + ".lock"
}

// load reads the registry from path without pruning. A missing file yields an
// empty registry. A malformed file is treated as empty (best-effort recovery)
// rather than an error, so a corrupted registry never breaks `sonar list`.
func load() *Registry {
	reg := &Registry{Runs: map[int]Entry{}}
	data, err := os.ReadFile(Path())
	if err != nil {
		return reg
	}
	if err := json.Unmarshal(data, reg); err != nil || reg.Runs == nil {
		return &Registry{Runs: map[int]Entry{}}
	}
	return reg
}

// Load reads the registry and prunes entries whose PID is no longer alive
// (stale after a crash or hard kill). Pruned entries are written back to disk
// so the file self-heals. Pruning failures to persist are ignored.
func Load() *Registry {
	reg := load()
	pruned := false
	for pid := range reg.Runs {
		if !pidAlive(pid) {
			delete(reg.Runs, pid)
			pruned = true
		}
	}
	if pruned {
		_ = withLock(func() error {
			// Re-read under lock and re-prune to avoid racing a concurrent add.
			fresh := load()
			for pid := range fresh.Runs {
				if !pidAlive(pid) {
					delete(fresh.Runs, pid)
				}
			}
			reg = fresh
			return save(fresh)
		})
	}
	return reg
}

// LookupByPID returns the entry for a PID if present.
func (r *Registry) LookupByPID(pid int) (Entry, bool) {
	e, ok := r.Runs[pid]
	return e, ok
}

// Active returns all live entries (pruning is already applied by Load).
func (r *Registry) Active() []Entry {
	out := make([]Entry, 0, len(r.Runs))
	for _, e := range r.Runs {
		out = append(out, e)
	}
	return out
}

// Add records (or replaces) an entry for e.PID, atomically and under lock.
func Add(e Entry) error {
	if e.PID <= 0 {
		return fmt.Errorf("runs: invalid pid %d", e.PID)
	}
	if e.StartedAt == "" {
		e.StartedAt = time.Now().Format(time.RFC3339)
	}
	return withLock(func() error {
		reg := load()
		reg.Runs[e.PID] = e
		return save(reg)
	})
}

// Remove deletes the entry for pid, atomically and under lock. Removing a
// missing pid is a no-op.
func Remove(pid int) error {
	return withLock(func() error {
		reg := load()
		if _, ok := reg.Runs[pid]; !ok {
			return nil
		}
		delete(reg.Runs, pid)
		return save(reg)
	})
}

// save writes the registry atomically: marshal -> temp file -> rename. Callers
// must already hold the lock (see withLock).
func save(reg *Registry) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("runs: could not create config dir: %w", err)
	}
	if reg.Runs == nil {
		reg.Runs = map[int]Entry{}
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("runs: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runs-*.json")
	if err != nil {
		return fmt.Errorf("runs: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("runs: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("runs: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("runs: rename: %w", err)
	}
	return nil
}
