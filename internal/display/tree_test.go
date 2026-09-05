package display

import (
	"bytes"
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
