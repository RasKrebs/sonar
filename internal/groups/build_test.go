package groups

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/raskrebs/sonar/internal/state"
)

func TestGroupsFromPortsOnly(t *testing.T) {
	f := newFixture(t)
	index := NewIndex()
	index.Observe(f.repoSub)

	pp := Resolve([]state.Port{
		nativePort(8123, f.repoSub),
		nativePort(8124, f.repo),
		nativePort(3000, ""),
	}, NoPins{}, NoRuns{}, index)

	got := Groups(pp, index)
	if len(got) != 1 {
		t.Fatalf("groups = %+v, want one", got)
	}
	g := got[0]
	if g.Name != "sonar" || g.Source != state.SourceAuto || g.Status != "running" {
		t.Fatalf("group = %+v", g)
	}
	if len(g.Members) != 2 || g.Members[0] != 8123 || g.Members[1] != 8124 {
		t.Errorf("members = %v, want [8123 8124] sorted", g.Members)
	}
	if g.RootDir == nil || *g.RootDir != f.repo {
		t.Errorf("root_dir = %v, want %q", g.RootDir, f.repo)
	}
	if g.ConfigPath != nil {
		t.Errorf("config_path = %v, want null with no config", g.ConfigPath)
	}
}

func TestGroupsStatusFromServices(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: sonar\nservices:\n  - name: api\n    port: 8000\n  - name: web\n    port: 5173\n  - name: worker\n")

	tests := []struct {
		name       string
		listening  []state.Port
		wantStatus string
		wantUp     []string
	}{
		{"nothing running", nil, "stopped", nil},
		{
			"one of three", []state.Port{nativePort(8000, f.repo)},
			"partial", []string{"api"},
		},
		{
			"all three, the last matched by name",
			[]state.Port{
				nativePort(8000, f.repo), nativePort(5173, f.repo),
				{Port: 9000, Cwd: f.repo, DisplayName: "worker"},
			},
			"running", []string{"api", "web", "worker"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := NewIndex()
			index.Observe(f.repo)
			pp := Resolve(tt.listening, NoPins{}, NoRuns{}, index)
			got := Groups(pp, index)
			if len(got) != 1 {
				t.Fatalf("groups = %+v", got)
			}
			g := got[0]
			if g.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", g.Status, tt.wantStatus)
			}
			if g.Source != state.SourceFile {
				t.Errorf("source = %q, want file", g.Source)
			}
			if g.ConfigPath == nil || *g.ConfigPath != filepath.Join(f.repo, ConfigName) {
				t.Errorf("config_path = %v", g.ConfigPath)
			}
			if len(g.Services) != 3 {
				t.Fatalf("services = %+v", g.Services)
			}
			var up []string
			for _, s := range g.Services {
				if s.Running {
					up = append(up, s.Name)
					if s.PortActual == nil {
						t.Errorf("service %s is running with no port_actual", s.Name)
					}
				}
			}
			if len(up) != len(tt.wantUp) {
				t.Fatalf("running services = %v, want %v", up, tt.wantUp)
			}
			for i := range up {
				if up[i] != tt.wantUp[i] {
					t.Fatalf("running services = %v, want %v", up, tt.wantUp)
				}
			}
		})
	}
}

func TestGroupsTakeTheStrongestSource(t *testing.T) {
	f := newFixture(t)
	index := NewIndex()
	index.Observe(f.repo)
	pins := fakePins{9000: "sonar"}

	pp := Resolve([]state.Port{nativePort(8123, f.repo), nativePort(9000, f.repo)}, pins, NoRuns{}, index)
	got := Groups(pp, index)
	if len(got) != 1 || got[0].Source != state.SourceManual {
		t.Fatalf("groups = %+v, want one group with source manual", got)
	}
}

// TestGroupsValidateAgainstTheSchema keeps the group builder inside the
// published contract: whatever `sonar groups --json` prints must satisfy the
// checked-in Group definition.
func TestGroupsValidateAgainstTheSchema(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.repo, ConfigName),
		"name: sonar\nservices:\n  - name: api\n    port: 8000\n    health: /health\n    depends_on: [db]\n  - name: db\n    port: 5432\n")
	index := NewIndex()
	index.Observe(f.repo)
	pp := Resolve([]state.Port{nativePort(8000, f.repo), nativePort(3000, "")}, NoPins{}, NoRuns{}, index)

	sch := compileDefinition(t, "Group")
	for _, g := range Groups(pp, index) {
		if err := sch.Validate(roundTrip(t, g)); err != nil {
			t.Errorf("group %s does not validate:\n%v", g.Name, err)
		}
	}
}

func compileDefinition(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "schema", "protocol.schema.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("protocol.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("protocol.schema.json#/definitions/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}
