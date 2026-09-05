package groups

import (
	"fmt"

	"github.com/raskrebs/sonar/internal/state"
)

// Pins supplies manual group assignments (`sonar assign`), the top of the
// precedence chain. The store step (1A.4) provides the persistent
// implementation; NoPins stands in until then.
type Pins interface {
	// Group returns the pinned group for a port, matched against the port's
	// match keys (see MatchKeys).
	Group(p state.Port) (string, bool)
}

// NoPins is the empty pin set.
type NoPins struct{}

// Group never matches.
func (NoPins) Group(state.Port) (string, bool) { return "", false }

// Registry attributes a port to a process sonar started. The scanner has
// already walked the PPID ancestry against `runs.json`, so the default
// implementation just reads what it recorded.
type Registry interface {
	// Run returns the group and name of the run that owns this port.
	Run(p state.Port) (group, name string, ok bool)
}

// PortRuns reads the run attribution the scanner put on the port itself.
type PortRuns struct{}

// Run returns the run's group and name. Until `sonar start` records a group
// (step 1A.5, contract §11.3) run.group is empty, so this reports ok only for
// the name; the resolver then falls through to the file and git-root rules.
func (PortRuns) Run(p state.Port) (group, name string, ok bool) {
	if p.Run == nil {
		return "", "", false
	}
	return p.Run.Group, p.Run.Name, true
}

// NoRuns is the empty run registry.
type NoRuns struct{}

// Run never matches.
func (NoRuns) Run(state.Port) (string, string, bool) { return "", "", false }

// MatchKeys returns the keys a rename or a pin can be stored under for this
// port, most stable first. The store step matches a stored key against all of
// them on every scan so a label survives a restart or a new PID.
func MatchKeys(p state.Port) []string {
	var keys []string
	if p.Run != nil && p.Run.Name != "" {
		group := p.Run.Group
		if group == "" {
			group = "-"
		}
		keys = append(keys, fmt.Sprintf("run:%s/%s", group, p.Run.Name))
	}
	if p.Docker != nil {
		if p.Docker.ComposeProject != "" && p.Docker.ComposeService != "" {
			keys = append(keys, fmt.Sprintf("docker:%s/%s", p.Docker.ComposeProject, p.Docker.ComposeService))
		} else if p.Docker.Container != "" {
			keys = append(keys, "docker:"+p.Docker.Container)
		}
	}
	if p.ProjectRoot != nil && *p.ProjectRoot != "" {
		keys = append(keys, fmt.Sprintf("cwd:%s:%d", *p.ProjectRoot, p.Port))
	}
	keys = append(keys, fmt.Sprintf("port:%d", p.Port))
	return keys
}

// Resolve fills Group, GroupSource and ProjectRoot on a copy of pp, applying
// the precedence chain from the daemon spec, first match wins:
//
//  1. manual — a pin from `sonar assign`
//  2. start  — a `sonar start` run that owns the process
//  3. file   — a known `.sonar.yaml` that claims the port
//  4. compose — the Compose project, unless its working directory is inside a
//     git checkout, in which case the container merges into that checkout's
//     group so a Compose db and a native api are one group
//  5. gitroot — the checkout containing the process cwd, named `<repo>` or
//     `<repo>@<worktree>`; a `.sonar.yaml` at that root renames the group and
//     makes the source `file`
//  6. none — group stays null
//
// pins, runs and index may all be nil.
func Resolve(pp []state.Port, pins Pins, runs Registry, index *Index) []state.Port {
	out := make([]state.Port, len(pp))
	copy(out, pp)
	if index == nil {
		index = NewIndex()
	}
	for i := range out {
		resolveOne(&out[i], pins, runs, index)
	}
	return out
}

func resolveOne(p *state.Port, pins Pins, runs Registry, index *Index) {
	root, worktree := projectRoot(p, index)
	if root != "" {
		r := root
		p.ProjectRoot = &r
	}
	p.Group, p.GroupSource = nil, nil

	if pins != nil {
		if g, ok := pins.Group(*p); ok && g != "" {
			assign(p, g, state.SourceManual)
			return
		}
	}
	if runs != nil {
		if g, _, ok := runs.Run(*p); ok && g != "" {
			assign(p, g, state.SourceStart)
			return
		}
	}
	if cfg, _, ok := index.MatchPort(*p); ok {
		assign(p, cfg.Name, state.SourceFile)
		return
	}
	if root != "" {
		if cfg := index.Nearest(root); cfg != nil {
			assign(p, cfg.Name, state.SourceFile)
			return
		}
		assign(p, GroupName(root, worktree), state.SourceAuto)
		return
	}
	if p.Docker != nil && p.Docker.ComposeProject != "" {
		assign(p, p.Docker.ComposeProject, state.SourceAuto)
	}
}

// projectRoot is the checkout a port belongs to: the git root above the
// process cwd, or — for a Compose container, which has no cwd of its own — the
// git root above the project's working directory.
func projectRoot(p *state.Port, index *Index) (root, worktree string) {
	if p.Cwd != "" {
		if root, worktree, ok := Find(p.Cwd); ok {
			return root, worktree
		}
	}
	if p.Docker != nil && p.Docker.ComposeProject != "" {
		if dir := index.ComposeDir(p.Docker.ComposeProject); dir != "" {
			if root, worktree, ok := Find(dir); ok {
				return root, worktree
			}
		}
	}
	return "", ""
}

func assign(p *state.Port, name string, source state.GroupSource) {
	if name == "" {
		return
	}
	n, s := name, source
	p.Group, p.GroupSource = &n, &s
}
