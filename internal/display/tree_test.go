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
