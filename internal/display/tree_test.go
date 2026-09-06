package display

import (
	"bytes"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// treeFixture is one Compose service, one native service and one ungrouped
// port, with the group metadata the resolver would produce for them.
func treeFixture() ([]ports.ListeningPort, []state.Group) {
	pp := []ports.ListeningPort{
		{
			Port: 5432, BindAddress: "0.0.0.0", Process: "com.docke", Type: ports.PortTypeDocker,
			Group: "sonar", GroupSource: "file",
			DockerContainer: "sonar-db-1", DockerImage: "postgres:17",
			DockerComposeService: "db", DockerComposeProject: "sonar",
		},
		{Port: 8000, BindAddress: "127.0.0.1", Process: "python3", Group: "sonar", GroupSource: "file"},
		{Port: 3000, BindAddress: "127.0.0.1", Process: "node"},
	}
	gg := []state.Group{{
		Name:       "sonar",
		Source:     state.SourceFile,
		RootDir:    strp("/code/sonar"),
		ConfigPath: strp("/code/sonar/.sonar.yaml"),
		Status:     "partial",
		Members:    []int{5432, 8000},
		Services: []state.Service{
			{Name: "api", Port: intp(8000), Running: true, PortActual: intp(8000)},
			{Name: "db", Port: intp(5432), Running: true, PortActual: intp(5432)},
			{Name: "worker", DependsOn: []string{"db"}},
		},
	}}
	return pp, gg
}

func TestRenderTree(t *testing.T) {
	NoColor = true
	pp, gg := treeFixture()

	var buf bytes.Buffer
	RenderTree(&buf, pp, gg)

	want := `sonar  (2 ports, partial)   /code/sonar
├─ 5432  db    postgres:17  http://localhost:5432
└─ 8000  api   python3      http://127.0.0.1:8000

ungrouped  (1 port)
└─ 3000  node               http://127.0.0.1:3000

3 ports in 1 group
`
	if got := buf.String(); got != want {
		t.Errorf("RenderTree =\n%s\n---- want ----\n%s", got, want)
	}
}

// TestRenderTreeCollapsesDualStackRows: a service listening on both families
// is one entry in the tree, and the port count says one. Windows shows this
// most — every listener there comes back as an IPv4 row and an IPv6 row — and
// the tree printed each one twice, under a heading claiming twice the ports.
func TestRenderTreeCollapsesDualStackRows(t *testing.T) {
	NoColor = true
	pp := []ports.ListeningPort{
		{Port: 8000, BindAddress: "127.0.0.1", PID: 42, Process: "python3", Group: "api"},
		{Port: 8000, BindAddress: "::1", PID: 42, Process: "python3", Group: "api"},
		// Same port, a different process: two listeners, two rows.
		{Port: 9000, BindAddress: "127.0.0.1", PID: 7, Process: "node", Group: "api"},
		{Port: 9000, BindAddress: "127.0.0.2", PID: 8, Process: "node", Group: "api"},
	}

	var buf bytes.Buffer
	RenderTree(&buf, pp, nil)
	got := buf.String()

	rows := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "├─") || strings.HasPrefix(line, "└─") {
			rows++
		}
	}
	if rows != 3 {
		t.Errorf("the tree printed %d rows, want 3:\n%s", rows, got)
	}
	if n := strings.Count(got, ":8000"); n != 1 {
		t.Errorf("port 8000 appears %d times, want once:\n%s", n, got)
	}
	// Same port, two pids: two listeners, and collapsing them would hide a
	// genuine conflict.
	if n := strings.Count(got, ":9000"); n != 2 {
		t.Errorf("port 9000 appears %d times, want twice (two pids):\n%s", n, got)
	}
	if !strings.Contains(got, "3 ports") {
		t.Errorf("the summary does not count the rows it printed:\n%s", got)
	}
	// The first row for a pair wins, so the IPv4 address is the one shown.
	if !strings.Contains(got, "http://127.0.0.1:8000") {
		t.Errorf("the surviving row is not the first one:\n%s", got)
	}
}

func TestRenderTreeEmpty(t *testing.T) {
	NoColor = true
	var buf bytes.Buffer
	RenderTree(&buf, nil, nil)
	if got, want := buf.String(), "No listening ports found.\n"; got != want {
		t.Errorf("RenderTree(empty) = %q, want %q", got, want)
	}
}

func TestRenderGroups(t *testing.T) {
	NoColor = true
	_, gg := treeFixture()

	var buf bytes.Buffer
	RenderGroups(&buf, gg)

	want := `NAME    SOURCE   ROOT          PORTS       STATUS
sonar   file     /code/sonar   5432,8000   partial

1 group
`
	if got := buf.String(); got != want {
		t.Errorf("RenderGroups =\n%s\n---- want ----\n%s", got, want)
	}
}

func TestRenderGroup(t *testing.T) {
	NoColor = true
	pp, gg := treeFixture()

	var buf bytes.Buffer
	RenderGroup(&buf, gg[0], pp)

	want := `sonar  (source: file, status: partial)
root:    /code/sonar
config:  /code/sonar/.sonar.yaml

PORTS
  5432   db                    http://localhost:5432
  8000   python3               http://127.0.0.1:8000

SERVICES
  api               8000   running
  db                5432   running
  worker                   stopped  after db
`
	if got := buf.String(); got != want {
		t.Errorf("RenderGroup =\n%s\n---- want ----\n%s", got, want)
	}
}

// TestRenderGroupShowsServiceMetadata: the description, icon and colour a
// project writes into its `.sonar.yaml` are shown, and a service without them
// prints exactly as it did before (step 1A.7).
func TestRenderGroupShowsServiceMetadata(t *testing.T) {
	desc, icon, color := "The HTTP API", "*", "blue"
	g := state.Group{
		Name: "demo", Source: state.SourceFile, Status: "partial",
		Services: []state.Service{
			{Name: "api", Description: &desc, Icon: &icon, Color: &color, DependsOn: []string{}},
			{Name: "db", DependsOn: []string{}},
		},
	}

	var buf bytes.Buffer
	RenderGroup(&buf, g, nil)
	out := buf.String()

	for _, want := range []string{"* api", "The HTTP API", "color blue"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "db") && (strings.Contains(line, "color") || strings.Contains(line, "*")) {
			t.Errorf("a service without metadata should print plainly, got %q", line)
		}
	}
}
