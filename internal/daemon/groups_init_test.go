package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// initHarness gives a test a daemon that can see two services listening inside
// a git checkout that has no `.sonar.yaml` yet — the state `groups.init` exists
// to get out of.
func initHarness(t *testing.T, ctx context.Context) (*testHarness, string, string) {
	t.Helper()
	root := resolvedDir(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, ctx)
	h.setRows(
		ports.ListeningPort{
			Port: 8000, BindAddress: "127.0.0.1", PID: 4242,
			Process: "python", Display: "api", Command: "uv run api", Cwd: root,
		},
		ports.ListeningPort{
			Port: 5173, BindAddress: "127.0.0.1", PID: 4243,
			Process: "node", Display: "web", Command: "npm run dev",
			Cwd: filepath.Join(root, "web"),
		},
		// Somebody else's process, in somebody else's directory: it must not
		// end up in this project's file.
		ports.ListeningPort{
			Port: 9999, BindAddress: "127.0.0.1", PID: 4244,
			Process: "other", Display: "other", Command: "other", Cwd: resolvedDir(t, t.TempDir()),
		},
	)
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the scanner: %v", err)
	}
	return h, root, filepath.Join(root, groups.ConfigName)
}

// callInit is the method under test, with the params a caller sends.
func callInit(t *testing.T, c *testClient, p rpc.GroupsInitParams) (rpc.GroupsInitResult, *rpc.Error) {
	t.Helper()
	var res rpc.GroupsInitResult
	e := c.call("groups.init", p, &res)
	return res, e
}

// TestGroupsInitDryRunReturnsAParsableProposal is the default call: no write,
// and what comes back is the exact file that would have been written.
func TestGroupsInitDryRunReturnsAParsableProposal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root})
	if e != nil {
		t.Fatalf("groups.init: %v", e)
	}
	if !res.OK || res.Path != target {
		t.Fatalf("result = %+v, want ok with path %q", res, target)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("a dry run must not write %s (err = %v)", target, err)
	}
	if !strings.Contains(res.YAML, "sonar init") {
		t.Errorf("yaml is missing the generated header:\n%s", res.YAML)
	}

	// The proposal has to survive the loader, or `write: true` would put an
	// unusable file on disk.
	cfg := loadYAML(t, res.YAML)
	if cfg.Name != res.Proposal.Name {
		t.Errorf("parsed name %q, proposal says %q", cfg.Name, res.Proposal.Name)
	}
	if got := servicePorts(t, res.YAML); len(got) != 2 || got[0] != 5173 || got[1] != 8000 {
		t.Fatalf("proposed ports = %v, want the two inside the checkout", got)
	}
	if len(res.Affected) != 2 {
		t.Errorf("affected = %v, want the two proposed service names", res.Affected)
	}
	if res.Proposal.ConfigPath == nil || *res.Proposal.ConfigPath != target {
		t.Errorf("proposal config_path = %v, want %q", res.Proposal.ConfigPath, target)
	}
	if len(res.Proposal.Members) != 2 || res.Proposal.Status != "running" {
		t.Errorf("proposal group = %+v, want the two running members", res.Proposal)
	}
}

// TestGroupsInitWriteFlipsTheGroupToFile is the whole point of the method: the
// file lands on disk and the next read already calls the group a file group.
func TestGroupsInitWriteFlipsTheGroupToFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true})
	if e != nil {
		t.Fatalf("groups.init: %v", e)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading what the daemon wrote: %v", err)
	}
	if string(onDisk) != res.YAML {
		t.Errorf("the file and the returned yaml differ:\n%s\n---\n%s", onDisk, res.YAML)
	}

	var list rpc.GroupsListResult
	if e := c.call("groups.list", nil, &list); e != nil {
		t.Fatalf("groups.list: %v", e)
	}
	g, ok := groupNamed(list.Groups, res.Proposal.Name)
	if !ok {
		t.Fatalf("group %q is gone after init: %+v", res.Proposal.Name, list.Groups)
	}
	if g.Source != "file" {
		t.Errorf("group source = %q, want file", g.Source)
	}
	if g.ConfigPath == nil || *g.ConfigPath != target {
		t.Errorf("group config_path = %v, want %q", g.ConfigPath, target)
	}
	if len(g.Services) != 2 {
		t.Fatalf("group services = %+v, want the two just written", g.Services)
	}
	for _, svc := range g.Services {
		if !svc.Running {
			t.Errorf("service %q should be running: %+v", svc.Name, svc)
		}
	}
}

// TestGroupsInitRefusesToOverwrite keeps §16's rule on the wire: an existing
// file is never clobbered by accident, and the error names the way past it.
func TestGroupsInitRefusesToOverwrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	const hand = "name: hand-written\n"
	if err := os.WriteFile(target, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}

	// A dry run still answers: reading is never blocked by an existing file.
	if _, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root}); e != nil {
		t.Fatalf("dry run over an existing file: %v", e)
	}

	_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true})
	if e == nil {
		t.Fatal("writing over an existing file should have been refused")
	}
	if e.Data.Code != "invalid_params" || !strings.Contains(e.Data.Hint, "force") {
		t.Errorf("error = %+v, want invalid_params hinting at force", e.Data)
	}
	if b, _ := os.ReadFile(target); string(b) != hand {
		t.Errorf("the file was touched anyway:\n%s", b)
	}
}

// TestGroupsInitForceOverwrites is the other half of the same rule.
func TestGroupsInitForceOverwrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, target := initHarness(t, ctx)
	c := h.dial(ctx)

	if err := os.WriteFile(target, []byte("name: hand-written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, e := callInit(t, c, rpc.GroupsInitParams{RootDir: root, Write: true, Force: true})
	if e != nil {
		t.Fatalf("groups.init --force: %v", e)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != res.YAML || strings.Contains(string(b), "hand-written") {
		t.Errorf("force did not replace the file:\n%s", b)
	}
}

// TestGroupsInitRejectsABadRoot: the daemon has no working directory of the
// caller's, so a root it cannot use is a parameter error, not a guess.
func TestGroupsInitRejectsABadRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, root, _ := initHarness(t, ctx)
	c := h.dial(ctx)

	file := filepath.Join(root, "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		root string
		code string
	}{
		{"missing", "", "invalid_params"},
		{"relative", filepath.Join("relative", "path"), "invalid_params"},
		{"nonexistent", filepath.Join(root, "nope", "nope"), "not_found"},
		{"not a directory", file, "invalid_params"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := callInit(t, c, rpc.GroupsInitParams{RootDir: tc.root, Write: true})
			if e == nil {
				t.Fatalf("root_dir %q should have been refused", tc.root)
			}
			if e.Data.Code != tc.code {
				t.Errorf("error code = %q, want %q (%s)", e.Data.Code, tc.code, e.Data.Detail)
			}
		})
	}
}

// loadYAML writes a proposal to a scratch file and reads it back through the
// real loader, which is the check that matters: the daemon must never propose
// something `groups.Load` would reject.
func loadYAML(t *testing.T, text string) *groups.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), groups.ConfigName)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := groups.Load(path)
	if err != nil {
		t.Fatalf("the proposal does not load: %v\n%s", err, text)
	}
	return cfg
}

// servicePorts is the proposed ports, sorted, read back out of the YAML.
func servicePorts(t *testing.T, text string) []int {
	t.Helper()
	cfg := loadYAML(t, text)
	out := make([]int, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		out = append(out, s.Port)
	}
	return out
}

func groupNamed(gg []state.Group, name string) (state.Group, bool) {
	for _, g := range gg {
		if g.Name == name {
			return g, true
		}
	}
	return state.Group{}, false
}
