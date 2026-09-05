package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/raskrebs/sonar/internal/state"
)

// osPath turns a slash path from a test table into the host's spelling, so the
// same tables cover Windows. cwdKey is the key MatchKeys should build for it.
func osPath(p string) string { return filepath.FromSlash(p) }

func cwdKey(root string, port int) string {
	return fmt.Sprintf("%s%s:%d", KeyPrefixCwd, osPath(root), port)
}

func strptr(s string) *string { return &s }

// port builds a minimal state.Port for key tests.
func port(n int, opts ...func(*state.Port)) state.Port {
	p := state.Port{Port: n, BindAddress: "127.0.0.1", PID: 1000 + n}
	for _, o := range opts {
		o(&p)
	}
	return p
}

func withRun(group, name string, rootPID int) func(*state.Port) {
	return func(p *state.Port) {
		p.Run = &state.Run{ID: "abc", Group: group, Name: name, RootPID: rootPID}
	}
}

func withDocker(d state.Docker) func(*state.Port) {
	return func(p *state.Port) { p.Docker = &d }
}

func withRoot(root string) func(*state.Port) {
	return func(p *state.Port) { p.ProjectRoot = strptr(root) }
}

func withCwd(cwd string) func(*state.Port) {
	return func(p *state.Port) { p.Cwd = cwd }
}

func withPID(pid int) func(*state.Port) {
	return func(p *state.Port) { p.PID = pid }
}

func TestMatchKeys(t *testing.T) {
	cases := []struct {
		name string
		port state.Port
		want []string
	}{
		{
			name: "run wins over everything",
			port: port(5173, withRun("sonar", "frontend", 48190),
				withRoot(osPath("/Users/me/code/sonar")), withCwd(osPath("/Users/me/code/sonar/frontend"))),
			want: []string{
				"run:sonar/frontend",
				cwdKey("/Users/me/code/sonar", 5173),
				"port:5173",
			},
		},
		{
			name: "compose project and service",
			port: port(5432, withDocker(state.Docker{
				Container: "shop-db-1", ComposeProject: "shop", ComposeService: "db",
			})),
			want: []string{"docker:shop/db", "port:5432"},
		},
		{
			name: "container without compose labels",
			port: port(6379, withDocker(state.Docker{Container: "redis-cache"})),
			want: []string{"docker:redis-cache", "port:6379"},
		},
		{
			name: "native process by project root",
			port: port(3000, withRoot(osPath("/Users/me/code/shop")), withCwd(osPath("/Users/me/code/shop/api"))),
			want: []string{cwdKey("/Users/me/code/shop", 3000), "port:3000"},
		},
		{
			name: "cwd stands in when there is no project root",
			port: port(3000, withCwd(osPath("/tmp/scratch/"))),
			want: []string{cwdKey("/tmp/scratch", 3000), "port:3000"},
		},
		{
			name: "port only when there is no cwd at all",
			port: port(8080),
			want: []string{"port:8080"},
		},
		{
			name: "run without a name falls through",
			port: port(9000, withRun("sonar", "", 1)),
			want: []string{"port:9000"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchKeys(tc.port)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MatchKeys = %q, want %q", got, tc.want)
			}
			if PrimaryKey(tc.port) != tc.want[0] {
				t.Errorf("PrimaryKey = %q, want %q", PrimaryKey(tc.port), tc.want[0])
			}
		})
	}
}

// A rename has to survive a restart: same run, brand new PID, and even a new
// port, still the same key.
func TestMatchKeysSurviveRestart(t *testing.T) {
	before := port(5173, withRun("sonar", "frontend", 48190), withRoot(osPath("/code/sonar")))
	after := port(5173, withRun("sonar", "frontend", 51002), withRoot(osPath("/code/sonar")), withPID(51002))

	if PrimaryKey(before) != PrimaryKey(after) {
		t.Errorf("run key changed across restart: %q -> %q", PrimaryKey(before), PrimaryKey(after))
	}

	onNewPort := port(5174, withRun("sonar", "frontend", 51002))
	if PrimaryKey(onNewPort) != PrimaryKey(before) {
		t.Errorf("run key changed when the port moved: %q -> %q",
			PrimaryKey(before), PrimaryKey(onNewPort))
	}

	// A native process keyed by cwd survives a new PID too.
	nativeBefore := port(3000, withRoot(osPath("/code/shop")), withPID(100))
	nativeAfter := port(3000, withRoot(osPath("/code/shop")), withPID(200))
	if PrimaryKey(nativeBefore) != PrimaryKey(nativeAfter) {
		t.Errorf("cwd key changed across restart: %q -> %q",
			PrimaryKey(nativeBefore), PrimaryKey(nativeAfter))
	}
}

// The same port number in a different project is a different thing.
func TestMatchKeysSamePortDifferentProject(t *testing.T) {
	shop := port(3000, withRoot(osPath("/code/shop")))
	blog := port(3000, withRoot(osPath("/code/blog")))

	if PrimaryKey(shop) == PrimaryKey(blog) {
		t.Fatalf("two projects on port 3000 share the key %q", PrimaryKey(shop))
	}

	s := openTemp(t)
	if err := s.SetRename(PrimaryKey(shop), "storefront"); err != nil {
		t.Fatalf("SetRename: %v", err)
	}
	if name, ok, err := s.ResolveRename(shop); err != nil || !ok || name != "storefront" {
		t.Errorf("ResolveRename(shop) = %q, %v, %v; want storefront, true, nil", name, ok, err)
	}
	if name, ok, err := s.ResolveRename(blog); err != nil || ok {
		t.Errorf("ResolveRename(blog) = %q, %v, %v; want the shop rename not to leak", name, ok, err)
	}
}

func TestLookupUsesKeyPrecedence(t *testing.T) {
	p := port(5173, withRun("sonar", "frontend", 1), withRoot(osPath("/code/sonar")))

	m := map[string]string{"port:5173": "least", cwdKey("/code/sonar", 5173): "middle"}
	if got, ok := Lookup(m, p); !ok || got != "middle" {
		t.Errorf("Lookup = %q, %v; want middle, true", got, ok)
	}
	m["run:sonar/frontend"] = "most"
	if got, ok := Lookup(m, p); !ok || got != "most" {
		t.Errorf("Lookup = %q, %v; want most, true", got, ok)
	}
	if _, ok := Lookup(nil, p); ok {
		t.Error("Lookup on an empty map reported a hit")
	}
}
