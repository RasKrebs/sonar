package killer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// ---------------------------------------------------------------- harness ---

type signalCall struct {
	pid   int
	force bool
	group bool
}

// fakeClock advances only when the code under test sleeps, so an escalation
// test costs no wall-clock time and cannot flake on a slow machine.
type fakeClock struct {
	now   time.Time
	slept time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(d time.Duration) {
	c.now = c.now.Add(d)
	c.slept += d
}

// fakeWorld is a scriptable machine: which pids are alive, which ports answer,
// and a log of everything the killer did to it.
type fakeWorld struct {
	table   ProcessTable
	clock   *fakeClock
	alive   map[int]bool
	open    map[int]bool // port -> still listening
	groups  map[int]int  // pid -> pgid, for runs that lead their own group
	signals []signalCall
	stopped []string
	failKil map[int]error
	// killEffect runs after each successful signal so a test can describe how
	// the machine reacts (a process that dies on SIGTERM, one that ignores it).
	killEffect func(w *fakeWorld, c signalCall)
}

func newWorld(table ProcessTable) *fakeWorld {
	w := &fakeWorld{
		table:   table,
		clock:   &fakeClock{now: time.Unix(0, 0)},
		alive:   map[int]bool{},
		open:    map[int]bool{},
		groups:  map[int]int{},
		failKil: map[int]error{},
	}
	for pid := range table {
		w.alive[pid] = true
	}
	return w
}

func (w *fakeWorld) engine() *engine {
	return &engine{
		table:      w.table,
		clock:      w.clock,
		nativeTree: false,
		signalProc: func(pid int, force bool) error { return w.record(signalCall{pid: pid, force: force}) },
		signalGrp: func(pgid int, force bool) error {
			return w.record(signalCall{pid: pgid, force: force, group: true})
		},
		signalTree: func(pid int, force bool) error { return w.record(signalCall{pid: pid, force: force}) },
		groupOf: func(pid int) (int, bool) {
			pgid, ok := w.groups[pid]
			return pgid, ok
		},
		alive:      func(pid int) bool { return w.alive[pid] },
		portOpen:   func(port int, bind string) bool { return w.open[port] },
		dockerStop: func(name string) error { w.stopped = append(w.stopped, name); return nil },
	}
}

func (w *fakeWorld) record(c signalCall) error {
	w.signals = append(w.signals, c)
	if err := w.failKil[c.pid]; err != nil {
		return err
	}
	if w.killEffect != nil {
		w.killEffect(w, c)
	}
	return nil
}

// signalledPIDs returns the pids in the order they were signalled.
func (w *fakeWorld) signalledPIDs() []int {
	out := make([]int, 0, len(w.signals))
	for _, c := range w.signals {
		out = append(out, c.pid)
	}
	return out
}

// listener is a scanned port row with just the fields the killer reads.
func listener(port, pid int, opts ...func(*ports.ListeningPort)) ports.ListeningPort {
	lp := ports.ListeningPort{
		Port:        port,
		PID:         pid,
		BindAddress: "127.0.0.1",
		Process:     "node",
		Type:        ports.PortTypeUser,
	}
	for _, o := range opts {
		o(&lp)
	}
	return lp
}

func methods(results []Result) []state.KillMethod {
	out := make([]state.KillMethod, len(results))
	for i, r := range results {
		out[i] = r.Method
	}
	return out
}

func pidsOf(results []Result) []int {
	out := make([]int, len(results))
	for i, r := range results {
		out[i] = r.PID
	}
	return out
}

// ------------------------------------------------------------------ tests ---

func TestDryRunPlansTheTreeAndTouchesNothing(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Tree: true, DryRun: true})

	if got, want := pidsOf(results), []int{400, 401, 300, 200}; !reflect.DeepEqual(got, want) {
		t.Fatalf("planned pids = %v, want %v (children before parents)", got, want)
	}
	for _, r := range results {
		if r.Method != state.MethodSIGTERM || !r.OK {
			t.Errorf("row %+v: want a successful sigterm plan", r)
		}
		if r.Port != 3000 || r.BindAddress != "127.0.0.1" {
			t.Errorf("row %+v: every row carries the target port", r)
		}
	}
	if len(w.signals) != 0 || len(w.stopped) != 0 {
		t.Fatalf("dry run had side effects: signals=%v docker=%v", w.signals, w.stopped)
	}
	if w.clock.slept != 0 {
		t.Fatalf("dry run waited %v", w.clock.slept)
	}
}

func TestDryRunNamesEveryProcessInTheTree(t *testing.T) {
	w := newWorld(npmTree())
	snapshot := []ports.ListeningPort{listener(3000, 200)}
	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Tree: true, DryRun: true})

	want := []string{"esbuild", "esbuild", "node", "node"}
	got := make([]string, len(results))
	for i, r := range results {
		got[i] = r.Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestWithoutTreeOnlyTheListenerIsSignalled(t *testing.T) {
	w := newWorld(npmTree())
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{})

	if got := w.signalledPIDs(); !reflect.DeepEqual(got, []int{200}) {
		t.Fatalf("signalled %v, want only the listener", got)
	}
	if len(results) != 1 || results[0].Method != state.MethodSIGTERM {
		t.Fatalf("results = %+v", results)
	}
}

func TestEscalatesToSIGKILLWhenThePortStaysOpen(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true // the listener ignores SIGTERM
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Grace: time.Second})

	want := []signalCall{{pid: 200}, {pid: 200, force: true}}
	if !reflect.DeepEqual(w.signals, want) {
		t.Fatalf("signals = %+v, want SIGTERM then SIGKILL", w.signals)
	}
	if w.clock.slept != time.Second {
		t.Fatalf("waited %v, want the full one second grace", w.clock.slept)
	}
	if got := methods(results); !reflect.DeepEqual(got, []state.KillMethod{state.MethodSIGKILL}) {
		t.Fatalf("methods = %v, want the row rewritten to sigkill", got)
	}
}

func TestNoEscalationWhenThePortCloses(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true
	w.killEffect = func(w *fakeWorld, c signalCall) {
		// A well-behaved process closes its listener on the first SIGTERM.
		w.open[3000] = false
		w.alive[c.pid] = false
	}
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Grace: 5 * time.Second})

	if len(w.signals) != 1 || w.signals[0].force {
		t.Fatalf("signals = %+v, want a single SIGTERM", w.signals)
	}
	if w.clock.slept != 0 {
		t.Fatalf("waited %v, want no wait once the port is free", w.clock.slept)
	}
	if results[0].Method != state.MethodSIGTERM {
		t.Fatalf("method = %v, want sigterm", results[0].Method)
	}
}

func TestNoEscalateOptionLeavesTheProcessAlone(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true
	off := false
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Escalate: &off, Grace: time.Second})

	if len(w.signals) != 1 || w.signals[0].force {
		t.Fatalf("signals = %+v, want a single SIGTERM and no escalation", w.signals)
	}
	if w.clock.slept != 0 {
		t.Fatalf("waited %v with --no-escalate", w.clock.slept)
	}
	if results[0].Method != state.MethodSIGTERM {
		t.Fatalf("method = %v", results[0].Method)
	}
}

func TestForceSkipsStraightToSIGKILL(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Force: true})

	if len(w.signals) != 1 || !w.signals[0].force {
		t.Fatalf("signals = %+v, want one SIGKILL", w.signals)
	}
	if w.clock.slept != 0 {
		t.Fatalf("--force waited %v", w.clock.slept)
	}
	if results[0].Method != state.MethodSIGKILL {
		t.Fatalf("method = %v", results[0].Method)
	}
}

func TestEscalationOnlyHardKillsWhatIsStillAlive(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true // the parent hangs on to the socket
	w.killEffect = func(w *fakeWorld, c signalCall) {
		if c.pid != 200 { // the workers exit, npm does not
			w.alive[c.pid] = false
		}
	}
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}}, Options{Tree: true, Grace: time.Second})

	var hard []int
	for _, c := range w.signals {
		if c.force {
			hard = append(hard, c.pid)
		}
	}
	if !reflect.DeepEqual(hard, []int{200}) {
		t.Fatalf("SIGKILLed %v, want only the process still alive", hard)
	}
	if got := methods(results); !reflect.DeepEqual(got, []state.KillMethod{
		state.MethodSIGTERM, state.MethodSIGTERM, state.MethodSIGTERM, state.MethodSIGKILL,
	}) {
		t.Fatalf("methods = %v", got)
	}
}

func TestDockerContainersAreStoppedNotSignalled(t *testing.T) {
	w := newWorld(npmTree())
	snapshot := []ports.ListeningPort{listener(5432, 900, func(lp *ports.ListeningPort) {
		lp.Type = ports.PortTypeDocker
		lp.DockerContainer = "pg"
		lp.DockerComposeService = "db"
	})}

	results := w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 5432}}, Options{Tree: true, Force: true})

	if len(w.signals) != 0 {
		t.Fatalf("signalled a container: %+v", w.signals)
	}
	if !reflect.DeepEqual(w.stopped, []string{"pg"}) {
		t.Fatalf("stopped = %v, want [pg]", w.stopped)
	}
	if results[0].Method != state.MethodDockerStop || results[0].Name != "db" {
		t.Fatalf("row = %+v", results[0])
	}
}

func TestRunAttributedListenerKillsTheWholeRun(t *testing.T) {
	w := newWorld(npmTree())
	// vite listens, but sonar started the shell at pid 100.
	snapshot := []ports.ListeningPort{listener(3000, 300, func(lp *ports.ListeningPort) {
		lp.RunRootPID = 100
		lp.RunID = "abc123"
		lp.Tag = "web"
	})}

	// No --tree: run attribution implies the whole tree anyway.
	w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{})

	if got, want := w.signalledPIDs(), []int{400, 401, 300, 200, 201, 100}; !reflect.DeepEqual(got, want) {
		t.Fatalf("signalled %v, want the whole run tree children-first %v", got, want)
	}
}

func TestRunWithItsOwnProcessGroupIsSignalledAsAGroup(t *testing.T) {
	w := newWorld(npmTree())
	w.groups[100] = 100 // spawned with Setpgid
	snapshot := []ports.ListeningPort{listener(3000, 300, func(lp *ports.ListeningPort) {
		lp.RunRootPID = 100
	})}

	results := w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{})

	want := []signalCall{{pid: 100, group: true}}
	if !reflect.DeepEqual(w.signals, want) {
		t.Fatalf("signals = %+v, want one process-group signal", w.signals)
	}
	if len(results) != 1 || results[0].PID != 100 {
		t.Fatalf("results = %+v", results)
	}
}

func TestRunIDTargetExpandsToEveryPortOfTheRun(t *testing.T) {
	w := newWorld(npmTree())
	snapshot := []ports.ListeningPort{
		listener(3000, 300, func(lp *ports.ListeningPort) { lp.RunID = "abc"; lp.RunRootPID = 300 }),
		listener(24678, 300, func(lp *ports.ListeningPort) { lp.RunID = "abc"; lp.RunRootPID = 300 }),
		listener(5173, 999),
	}

	results := w.engine().kill(context.Background(), snapshot, []Target{{RunID: "abc"}}, Options{})

	// Both ports belong to the same tree; the shared pids are signalled once.
	if got := w.signalledPIDs(); !reflect.DeepEqual(got, []int{400, 401, 300}) {
		t.Fatalf("signalled %v", got)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v, want three rows (one per signalled process)", results)
	}
}

func TestOverlappingTargetsSignalEachProcessOnce(t *testing.T) {
	w := newWorld(npmTree())
	// 3000 is npm's tree; 3001 is vite inside that same tree.
	snapshot := []ports.ListeningPort{listener(3000, 200), listener(3001, 300)}

	w.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000}, {Port: 3001}}, Options{Tree: true})

	if got, want := w.signalledPIDs(), []int{400, 401, 300, 200}; !reflect.DeepEqual(got, want) {
		t.Fatalf("signalled %v, want %v: the inner tree first, no pid twice", got, want)
	}
}

func TestAmbiguousPortReportsTheBindAddresses(t *testing.T) {
	w := newWorld(npmTree())
	snapshot := []ports.ListeningPort{
		listener(3000, 200),
		listener(3000, 201, func(lp *ports.ListeningPort) { lp.BindAddress = "::1" }),
	}

	results := w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{})

	if len(results) != 1 || results[0].OK || results[0].Method != state.MethodNone {
		t.Fatalf("results = %+v, want one failed row", results)
	}
	if len(w.signals) != 0 {
		t.Fatalf("signalled despite ambiguity: %+v", w.signals)
	}

	// With --ip the same call resolves.
	w2 := newWorld(npmTree())
	results = w2.engine().kill(context.Background(), snapshot,
		[]Target{{Port: 3000, BindAddress: "::1"}}, Options{})
	if len(results) != 1 || !results[0].OK || results[0].PID != 201 {
		t.Fatalf("results = %+v", results)
	}
}

func TestUnknownPortIsANotFoundRow(t *testing.T) {
	w := newWorld(npmTree())
	results := w.engine().kill(context.Background(), nil, []Target{{Port: 9999}}, Options{})
	if len(results) != 1 || results[0].OK || results[0].Port != 9999 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Error == "" {
		t.Fatal("a failed row must carry its error")
	}
}

func TestHiddenListenerAsksForSudo(t *testing.T) {
	w := newWorld(npmTree())
	snapshot := []ports.ListeningPort{listener(3000, 0)}
	results := w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{})

	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v", results)
	}
	if len(w.signals) != 0 {
		t.Fatalf("signalled PID 0: %+v", w.signals)
	}
}

func TestSignalFailureIsReportedPerRow(t *testing.T) {
	w := newWorld(npmTree())
	w.failKil[200] = permissionErr(200, errors.New("operation not permitted"))
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	results := w.engine().kill(context.Background(), snapshot, []Target{{Port: 3000}}, Options{})

	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v", results)
	}
	if Code(w.failKil[200]) != CodePermissionDenied || Hint(w.failKil[200]) != "re-run with sudo" {
		t.Fatalf("permission errors must map to the contract code and the sudo hint")
	}
}

func TestPIDTargetWithoutAListeningPort(t *testing.T) {
	w := newWorld(npmTree())
	off := false
	results := w.engine().kill(context.Background(), nil, []Target{{PID: 300}},
		Options{Tree: true, Escalate: &off})

	if got := w.signalledPIDs(); !reflect.DeepEqual(got, []int{400, 401, 300}) {
		t.Fatalf("signalled %v", got)
	}
	if len(results) != 3 || results[0].Port != 0 {
		t.Fatalf("results = %+v, want portless rows", results)
	}
}

func TestPIDTargetEscalatesOnTheProcess(t *testing.T) {
	w := newWorld(npmTree()) // every pid stays alive: nothing reacts to SIGTERM
	w.engine().kill(context.Background(), nil, []Target{{PID: 400}}, Options{Grace: time.Second})

	want := []signalCall{{pid: 400}, {pid: 400, force: true}}
	if !reflect.DeepEqual(w.signals, want) {
		t.Fatalf("signals = %+v, want SIGTERM then SIGKILL", w.signals)
	}
}

func TestProxyTargetsAreRoutedToMapStop(t *testing.T) {
	w := newWorld(npmTree())
	results := w.engine().kill(context.Background(), nil, []Target{{ProxyID: "px1"}}, Options{})
	if len(results) != 1 || results[0].OK || results[0].Method != state.MethodNone {
		t.Fatalf("results = %+v", results)
	}
}

func TestEmptyTargetIsRejected(t *testing.T) {
	w := newWorld(npmTree())
	results := w.engine().kill(context.Background(), nil, []Target{{}}, Options{})
	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v", results)
	}
}

func TestCancelledContextStopsBeforeEscalation(t *testing.T) {
	w := newWorld(npmTree())
	w.open[3000] = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot := []ports.ListeningPort{listener(3000, 200)}

	w.engine().kill(ctx, snapshot, []Target{{Port: 3000}}, Options{Grace: time.Minute})

	if len(w.signals) != 1 || w.signals[0].force {
		t.Fatalf("signals = %+v, want the SIGTERM only", w.signals)
	}
}

func TestOptionDefaults(t *testing.T) {
	var o Options
	if o.grace() != DefaultGrace {
		t.Errorf("grace = %v, want %v", o.grace(), DefaultGrace)
	}
	if !o.escalating() {
		t.Error("the zero Options must escalate")
	}
	if (Options{Force: true}).escalating() {
		t.Error("--force is already the strongest signal; nothing to escalate to")
	}
}
