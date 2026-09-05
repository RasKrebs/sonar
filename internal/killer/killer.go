// Package killer is sonar's single implementation of "stop what is listening
// here". The CLI (`sonar kill`, and the `kill-all` / `down` aliases), the
// daemon's ports.kill / groups.kill, and therefore MCP and the desktop app all
// go through KillPorts, so the rules below hold everywhere:
//
//   - Docker-published ports are stopped with `docker stop`, never signalled.
//   - A listener sonar started is stopped by its process group when it has one
//     of its own, otherwise by walking the run root's process tree, so the
//     whole `npm → vite → esbuild` tree dies rather than just the leaf.
//   - A tree is always signalled children before parents, so a supervisor
//     cannot restart a worker we already stopped.
//   - SIGTERM first; if the port is still listening after the grace period,
//     SIGKILL — unless the caller opted out of escalation.
//   - A dry run reports exactly the actions a real run would take, and takes
//     none of them.
package killer

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// DefaultGrace is how long a target gets to shut down after SIGTERM before the
// killer escalates to SIGKILL (daemon spec: `--grace`, default 5s).
const DefaultGrace = 5 * time.Second

// pollInterval is how often the escalation wait re-checks a target.
const pollInterval = 100 * time.Millisecond

// Target addresses one thing to kill. Exactly one of Port, PID, RunID and
// ProxyID is set; BindAddress only disambiguates a Port that is bound to
// several addresses (contract §3 selectors).
type Target struct {
	Port        int
	BindAddress string
	PID         int
	RunID       string
	ProxyID     string
}

// Options controls how targets are killed. The zero value is the documented
// default: SIGTERM, escalate to SIGKILL after DefaultGrace, no tree walk
// beyond what a sonar-started run implies.
type Options struct {
	// Tree signals the target's whole process tree, not just the listener.
	// A listener attributed to a sonar-started run is always treated as a
	// tree, with or without this flag.
	Tree bool
	// Force sends SIGKILL immediately and skips the escalation wait.
	Force bool
	// Grace is how long to wait after SIGTERM before escalating. Zero means
	// DefaultGrace.
	Grace time.Duration
	// Escalate opts out of SIGTERM → SIGKILL escalation when set to false.
	// Nil means the default, which is to escalate.
	Escalate *bool
	// DryRun plans the actions and performs none of them.
	DryRun bool
	// Ports is an optional pre-collected scan (enriched with Docker and
	// process information). When nil, KillPorts takes its own scan.
	Ports []ports.ListeningPort
}

// grace resolves the escalation window.
func (o Options) grace() time.Duration {
	if o.Grace <= 0 {
		return DefaultGrace
	}
	return o.Grace
}

// escalating reports whether SIGTERM should be followed by SIGKILL.
func (o Options) escalating() bool {
	if o.Force {
		return false // already the strongest signal
	}
	return o.Escalate == nil || *o.Escalate
}

// Result is one row of the outcome: what was done (or, for a dry run, would be
// done) to a single process or container. It is the contract §3 kill row.
type Result = state.KillResult

// Clock is the killer's view of time, injected so escalation can be tested
// without waiting for a real grace period.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// engine holds the machine-facing dependencies so tests can substitute them.
type engine struct {
	table      ProcessTable
	clock      Clock
	signalProc func(pid int, force bool) error
	signalGrp  func(pgid int, force bool) error
	signalTree func(pid int, force bool) error
	groupOf    func(pid int) (int, bool)
	alive      func(pid int) bool
	portOpen   func(port int, bind string) bool
	dockerStop func(container string) error
	nativeTree bool
}

func newEngine() *engine {
	return &engine{
		table:      scanProcessTable(),
		clock:      realClock{},
		signalProc: signalProcess,
		signalGrp:  signalGroup,
		signalTree: signalTree,
		groupOf:    processGroup,
		alive:      pidAlive,
		portOpen:   probePort,
		dockerStop: docker.StopContainer,
		nativeTree: hasNativeTreeKill(),
	}
}

// KillPorts stops every target and returns one row per action, children before
// parents. It never returns an error: a target that could not be resolved or
// signalled comes back as a row with OK false, so partial failures are
// reportable per target (daemon spec, "Error handling").
func KillPorts(ctx context.Context, targets []Target, opts Options) []Result {
	snapshot := opts.Ports
	if snapshot == nil {
		snapshot = scanPorts()
	}
	return newEngine().kill(ctx, snapshot, targets, opts)
}

// scanPorts takes the enriched scan the killer needs to resolve targets: the
// Docker pass marks container-published ports, and Enrich fills in PPIDs, run
// attribution and display names.
func scanPorts() []ports.ListeningPort {
	found, err := ports.Scan()
	if err != nil {
		return nil
	}
	docker.EnrichPorts(found)
	ports.Enrich(found)
	return found
}

// unit is one resolved thing to kill: a container, a process group, or an
// ordered list of pids (children first, root last).
type unit struct {
	port      int
	bind      string
	name      string
	container string // non-empty for a Docker target
	pgid      int    // non-zero when the whole process group is signalled
	pids      []int  // children-first order; ignored when pgid or container is set
	root      int    // the listener (or run root) this unit is anchored on
	depth     int    // ancestry depth of root, used to order units children-first
	err       error  // resolution failure: emits a single failed row
	rows      []int  // indexes into the result slice, filled during execution
}

// kill runs the four phases: resolve, order, signal, escalate.
func (e *engine) kill(ctx context.Context, snapshot []ports.ListeningPort, targets []Target, opts Options) []Result {
	var units []*unit
	for _, t := range targets {
		units = append(units, e.resolve(t, snapshot, opts)...)
	}
	e.dedupe(units)
	e.order(units)

	results := make([]Result, 0, len(units))
	for _, u := range units {
		results = e.plan(u, opts, results)
	}
	if opts.DryRun {
		return results
	}

	for _, u := range units {
		e.execute(u, opts, results)
	}
	if opts.escalating() {
		e.escalate(ctx, units, opts, results)
	}
	return results
}

// resolve turns one selector into the units it addresses. A run id can address
// several listeners, so this returns a slice.
func (e *engine) resolve(t Target, snapshot []ports.ListeningPort, opts Options) []*unit {
	switch {
	case t.ProxyID != "":
		return []*unit{{
			name: t.ProxyID,
			err:  codedf(CodeInvalidSelector, "", "proxy %s must be stopped through map.stop", t.ProxyID),
		}}
	case t.RunID != "":
		var out []*unit
		for i := range snapshot {
			if snapshot[i].RunID == t.RunID || snapshot[i].Tag == t.RunID {
				out = append(out, e.unitFor(&snapshot[i], opts))
			}
		}
		if len(out) == 0 {
			return []*unit{{name: t.RunID, err: codedf(CodeNotFound, "", "no listening port belongs to run %s", t.RunID)}}
		}
		return out
	case t.Port > 0:
		matches := matchPort(snapshot, t.Port, t.BindAddress)
		switch len(matches) {
		case 0:
			where := strconv.Itoa(t.Port)
			if t.BindAddress != "" {
				where = t.BindAddress + ":" + where
			}
			return []*unit{{port: t.Port, bind: t.BindAddress,
				err: codedf(CodeNotFound, "", "no process is listening on %s", where)}}
		case 1:
			return []*unit{e.unitFor(matches[0], opts)}
		default:
			var addrs []string
			for _, m := range matches {
				addrs = append(addrs, m.BindAddress)
			}
			return []*unit{{port: t.Port,
				err: codedf(CodeAmbiguous, "re-run with --ip "+addrs[0], "port %d is bound to %d addresses: %s",
					t.Port, len(addrs), strings.Join(addrs, ", "))}}
		}
	case t.PID > 0:
		// A pid selector may or may not correspond to a listener we scanned.
		for i := range snapshot {
			if snapshot[i].PID == t.PID {
				return []*unit{e.unitFor(&snapshot[i], opts)}
			}
		}
		if !e.alive(t.PID) {
			return []*unit{{root: t.PID, err: codedf(CodeNotFound, "", "no process with PID %d", t.PID)}}
		}
		u := &unit{root: t.PID, name: e.table.Name(t.PID)}
		e.fillProcesses(u, t.PID, opts.Tree)
		return []*unit{u}
	}
	return []*unit{{err: codedf(CodeInvalidSelector, "", "empty target: set one of port, pid, run_id or proxy_id")}}
}

// unitFor builds the unit for a scanned listener.
func (e *engine) unitFor(lp *ports.ListeningPort, opts Options) *unit {
	u := &unit{port: lp.Port, bind: lp.BindAddress, name: lp.DisplayName()}

	if lp.Type == ports.PortTypeDocker && lp.DockerContainer != "" {
		u.container = lp.DockerContainer
		return u
	}
	if lp.PID <= 0 {
		u.err = &CodedError{
			Code:   CodePermissionDenied,
			Detail: fmt.Sprintf("could not resolve the process listening on port %d", lp.Port),
			Hint:   "re-run with sudo for full visibility",
		}
		return u
	}

	// A listener sonar started is killed as a whole run: its process group
	// when it leads one, otherwise its tree. Either way the supervisor and its
	// workers go together.
	root := lp.PID
	tree := opts.Tree
	if lp.RunRootPID > 0 {
		root = lp.RunRootPID
		tree = true
	}
	u.root = root
	if pgid, ok := e.groupOf(root); ok {
		u.pgid = pgid
		u.depth = len(e.table.Ancestors(root))
		return u
	}
	e.fillProcesses(u, root, tree)
	return u
}

// fillProcesses records the pids this unit will signal, children first.
func (e *engine) fillProcesses(u *unit, root int, tree bool) {
	u.root = root
	u.depth = len(e.table.Ancestors(root))
	switch {
	case !tree:
		u.pids = []int{root}
	case e.nativeTree:
		// taskkill /T terminates the tree in one call; there is no PPID table
		// to walk and no ordering for us to impose.
		u.pids = []int{root}
	default:
		u.pids = e.table.Descendants(root)
		if len(u.pids) == 0 {
			u.pids = []int{root}
		}
	}
}

// dedupe drops pids already claimed by an earlier unit so overlapping targets
// (a port and its own child port, `--all` over a whole tree) signal each
// process exactly once.
func (e *engine) dedupe(units []*unit) {
	claimed := map[int]bool{}
	for _, u := range units {
		if u.pgid != 0 {
			claimed[u.pgid] = true
			continue
		}
		kept := u.pids[:0]
		for _, pid := range u.pids {
			if claimed[pid] {
				continue
			}
			claimed[pid] = true
			kept = append(kept, pid)
		}
		u.pids = kept
	}
}

// order sorts units so a unit rooted deeper in the process tree runs before its
// own ancestor: children before parents across targets, not just within one.
// The sort is stable, so unrelated targets keep the caller's order.
func (e *engine) order(units []*unit) {
	sort.SliceStable(units, func(i, j int) bool { return units[i].depth > units[j].depth })
}

// plan appends this unit's rows in the order they will be acted on, and records
// where they landed so execute and escalate can update them in place.
func (e *engine) plan(u *unit, opts Options, results []Result) []Result {
	add := func(pid int, name string, action state.KillAction) {
		u.rows = append(u.rows, len(results))
		results = append(results, Result{
			Port:        u.port,
			BindAddress: u.bind,
			PID:         pid,
			Name:        name,
			Action:      action,
			OK:          true,
		})
	}

	switch {
	case u.err != nil:
		u.rows = append(u.rows, len(results))
		results = append(results, Result{
			Port: u.port, BindAddress: u.bind, PID: u.root, Name: u.name,
			Action: state.ActionNone, OK: false, Error: u.err.Error(),
		})
	case u.container != "":
		add(0, u.name, state.ActionDockerStop)
	case u.pgid != 0:
		add(u.pgid, u.name, signalAction(opts.Force))
	default:
		for _, pid := range u.pids {
			name := e.table.Name(pid)
			if pid == u.root && u.name != "" {
				name = u.name
			}
			add(pid, name, signalAction(opts.Force))
		}
	}
	return results
}

// execute performs the planned actions for one unit and records failures.
func (e *engine) execute(u *unit, opts Options, results []Result) {
	if u.err != nil {
		return
	}
	fail := func(row int, err error) {
		results[row].OK = false
		results[row].Error = err.Error()
	}

	switch {
	case u.container != "":
		if err := e.dockerStop(u.container); err != nil {
			fail(u.rows[0], err)
		}
	case u.pgid != 0:
		if err := e.signalGrp(u.pgid, opts.Force); err != nil {
			fail(u.rows[0], err)
		}
	default:
		for i, pid := range u.pids {
			var err error
			if e.nativeTree && opts.Tree {
				err = e.signalTree(pid, opts.Force)
			} else {
				err = e.signalProc(pid, opts.Force)
			}
			if err != nil {
				fail(u.rows[i], err)
			}
		}
	}
}

// signalAction names the signal a non-escalated kill sends.
func signalAction(force bool) state.KillAction {
	if force {
		return state.ActionSIGKILL
	}
	return state.ActionSIGTERM
}

// matchPort returns the scanned listeners on a port, filtered by bind address
// when one was given.
func matchPort(snapshot []ports.ListeningPort, port int, bind string) []*ports.ListeningPort {
	var out []*ports.ListeningPort
	for i := range snapshot {
		if snapshot[i].Port != port {
			continue
		}
		if bind != "" && snapshot[i].BindAddress != bind {
			continue
		}
		out = append(out, &snapshot[i])
	}
	return out
}

// probePort reports whether something still accepts connections on a port. A
// wildcard bind is probed on loopback, which is where a dev server is reached
// from anyway.
func probePort(port int, bind string) bool {
	host := bind
	switch host {
	case "", "0.0.0.0", "*":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Alive reports whether a process with this pid currently exists. Callers use
// it to tell a pid from a port when a bare number could be either.
func Alive(pid int) bool { return pidAlive(pid) }
