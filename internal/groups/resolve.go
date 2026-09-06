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

// Registry attributes a port to a process sonar started. It answers with the
// whole run, not just its group, because `run`, `display_name` and
// `group_source: start` all have to come out of one lookup: a daemon that
// resolved the group from its live registry but the run from the runs.json
// mirror could publish `group_source: "start"` next to a null `run`.
type Registry interface {
	// Run returns the run that owns this port.
	Run(p state.Port) (run state.Run, ok bool)
}

// SessionRegistry is the optional half of Registry: a registry that also knows
// which agent session started a run reports it here, and AttributeWith stamps
// it onto the port (spec 2 §3, contract §5). A registry that does not
// implement it simply publishes ports with a null session.
type SessionRegistry interface {
	Session(p state.Port) (session state.Session, ok bool)
}

// PortRuns reads the run attribution the scanner put on the port itself. It is
// the direct-scan path: no daemon, so `runs.json` is the only registry there is.
type PortRuns struct{}

// Run returns the run the scanner already attributed. Until `sonar start`
// records a group (step 1A.5, contract §11.3) run.group is empty, so this
// reports ok with only the name; the resolver then falls through to the file
// and git-root rules.
func (PortRuns) Run(p state.Port) (state.Run, bool) {
	if p.Run == nil {
		return state.Run{}, false
	}
	return *p.Run, true
}

// NoRuns is the empty run registry.
type NoRuns struct{}

// Run never matches.
func (NoRuns) Run(state.Port) (state.Run, bool) { return state.Run{}, false }

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
		if run, ok := runs.Run(*p); ok && run.Group != "" {
			assign(p, run.Group, state.SourceStart)
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
