package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raskrebs/sonar/internal/groups"
	"github.com/spf13/cobra"
)

// TestGroupsEditSubcommandsAreWired: the three commands hang off `sonar groups`
// and take exactly the arguments the README documents.
func TestGroupsEditSubcommandsAreWired(t *testing.T) {
	byName := map[string]*cobra.Command{}
	for _, c := range groupsCmd.Commands() {
		byName[c.Name()] = c
	}
	for _, want := range []string{"add", "rename", "remove"} {
		if byName[want] == nil {
			t.Fatalf("`sonar groups %s` is not registered", want)
		}
	}

	for _, tc := range []struct {
		cmd  *cobra.Command
		good []string
		bad  [][]string
	}{
		{byName["add"], []string{"demo", "worker"}, [][]string{{"demo"}, {"demo", "worker", "extra"}}},
		{byName["rename"], []string{"demo", "db", "database"}, [][]string{{"demo", "db"}, {"demo"}}},
		{byName["remove"], []string{"demo", "worker"}, [][]string{{"demo"}, {"demo", "a", "b"}}},
	} {
		if err := tc.cmd.Args(tc.cmd, tc.good); err != nil {
			t.Errorf("%s %v: %v", tc.cmd.Name(), tc.good, err)
		}
		for _, bad := range tc.bad {
			if err := tc.cmd.Args(tc.cmd, bad); err == nil {
				t.Errorf("%s %v was accepted", tc.cmd.Name(), bad)
			}
		}
	}

	// `sonar groups remove` is what the daemon calls it, but `rm` is what
	// fingers type.
	if got := byName["remove"].Aliases; len(got) != 1 || got[0] != "rm" {
		t.Errorf("remove aliases = %v, want rm", got)
	}
	// A port is how a service is found again, so `add` insists on one.
	if byName["add"].Flags().Lookup("port").Annotations[cobra.BashCompOneRequiredFlag] == nil {
		t.Error("`sonar groups add` should require --port")
	}
}

// TestLocalConfigFindsTheNearestFile is the fallback that lets these commands
// work in a project the daemon has never seen a process in.
func TestLocalConfigFindsTheNearestFile(t *testing.T) {
	dir := t.TempDir()
	// A git root, because that is how far the resolver walks up: the same rule
	// `sonar up` follows when it is given no group name.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "backend")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)

	cfg := localConfig()
	if cfg == nil {
		t.Fatal("no config found at or above the working directory")
	}
	if cfg.Name != "demo" {
		t.Errorf("config = %+v, want the demo group", cfg)
	}
}

// TestLocalConfigIsNilWithoutAFile: the fallback must not invent one.
func TestLocalConfigIsNilWithoutAFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if cfg := localConfig(); cfg != nil {
		t.Errorf("localConfig() = %+v, want nil", cfg)
	}
}
