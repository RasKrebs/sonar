package groups

import (
	"testing"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// pinSet is a Pins backed by a plain map from match key to group.
type pinSet map[string]string

func (p pinSet) Group(port state.Port) (string, bool) {
	for _, k := range MatchKeys(port) {
		if g, ok := p[k]; ok {
			return g, true
		}
	}
	return "", false
}

// runSet is a Registry that answers for one port number.
type runSet struct {
	port  int
	id    string
	group string
	name  string
}

func (r runSet) Run(p state.Port) (state.Run, bool) {
	if p.Port != r.port {
		return state.Run{}, false
	}
	return state.Run{ID: r.id, Group: r.group, Name: r.name, RootPID: p.PID}, true
}

func TestAttributeWithAppliesPinsAndRuns(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 3000, PID: 11, Process: "node", Command: "node server.js"},
		{Port: 4000, PID: 12, Process: "python3", Command: "python3 -m http.server"},
	}

	resolved, index := AttributeWith(pp,
		pinSet{"port:3000": "storefront"},
		runSet{port: 4000, id: "r1", group: "itest", name: "web"},
		nil)

	if index == nil {
		t.Fatal("AttributeWith returned a nil index")
	}
	if got := deref(resolved[0].Group); got != "storefront" {
		t.Errorf("port 3000 group = %q, want storefront", got)
	}
	if resolved[0].GroupSource == nil || *resolved[0].GroupSource != state.SourceManual {
		t.Errorf("port 3000 group_source = %v, want manual", resolved[0].GroupSource)
	}
	if got := deref(resolved[1].Group); got != "itest" {
		t.Errorf("port 4000 group = %q, want itest", got)
	}
	if resolved[1].GroupSource == nil || *resolved[1].GroupSource != state.SourceStart {
		t.Errorf("port 4000 group_source = %v, want start", resolved[1].GroupSource)
	}
	// The scanner rows are written back too, so the direct-scan path sees the
	// same attribution.
	if pp[0].Group != "storefront" || pp[0].GroupSource != "manual" {
		t.Errorf("scanner row = %q/%q, want storefront/manual", pp[0].Group, pp[0].GroupSource)
	}
}

// TestAttributeWithPublishesTheRegistrysRun pins the invariant the daemon
// broke: whatever the registry says owns a port has to reach the published row
// as `run` and as the name clients render, not only as the group. A registry
// answer that set `group_source: start` next to a null `run` is what a
// `sonar start` racing a scan tick used to publish.
func TestAttributeWithPublishesTheRegistrysRun(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 4000, PID: 12, Process: "listener", Command: "/tmp/listener 4000"},
	}

	resolved, _ := AttributeWith(pp, nil, runSet{port: 4000, id: "r1", group: "itest", name: "web"}, nil)

	if resolved[0].Run == nil {
		t.Fatal("run is null; the registry's attribution never reached the row")
	}
	want := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 12}
	if *resolved[0].Run != want {
		t.Errorf("run = %+v, want %+v", *resolved[0].Run, want)
	}
	if resolved[0].DisplayName != "web" {
		t.Errorf("display_name = %q, want the name the run gave it", resolved[0].DisplayName)
	}
	if resolved[0].GroupSource == nil || *resolved[0].GroupSource != state.SourceStart {
		t.Errorf("group_source = %v, want start", resolved[0].GroupSource)
	}
}

// TestAttributeWithLeavesTheScannersOwnRunAlone is the no-daemon path: there is
// no registry to ask, so whatever the scanner attributed from runs.json while
// enriching has to survive.
func TestAttributeWithLeavesTheScannersOwnRunAlone(t *testing.T) {
	pp := []ports.ListeningPort{
		{Port: 4000, PID: 12, Process: "listener", Tag: "web", RunGroup: "itest", RunID: "r1", RunRootPID: 9},
	}

	resolved, _ := AttributeWith(pp, nil, PortRuns{}, nil)

	if resolved[0].Run == nil || resolved[0].Run.Name != "web" || resolved[0].Run.RootPID != 9 {
		t.Fatalf("run = %+v, want the scanner's own attribution", resolved[0].Run)
	}
	if got := deref(resolved[0].Group); got != "itest" {
		t.Errorf("group = %q, want itest", got)
	}
}

func TestAttributeWithNilArgumentsIsAttributeWithoutTheWorkingDirectory(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 3000, PID: 11, Process: "node"}}
	resolved, index := AttributeWith(pp, nil, nil, nil)
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	if resolved[0].Group != nil {
		t.Errorf("group = %v, want nil for a port with no cwd", *resolved[0].Group)
	}
	if len(index.Configs()) != 0 {
		t.Errorf("index picked up %d configs, want none", len(index.Configs()))
	}
}
