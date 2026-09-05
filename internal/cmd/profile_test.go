package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/profile"
)

// The proposal `sonar profile export` prints has to be a file the rest of
// sonar can read back, so the test writes it where a config lives and loads it
// with the real validator.
func TestProfileExportProducesALoadableConfig(t *testing.T) {
	prof := &profile.Profile{
		Name: "my app",
		Ports: []profile.PortEntry{
			{Port: 8000, Name: "api", Health: true, HealthPath: "/healthz"},
			{Port: 5173, Name: "Frontend (vite)"},
			{Port: 5174, Name: "Frontend (vite)"},
			{Port: 5432, Name: "db", Health: true},
		},
	}

	out, err := groups.Marshal(profileConfig(prof))
	if err != nil {
		t.Fatalf("marshalling the proposal: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := groups.Load(path)
	if err != nil {
		t.Fatalf("the exported proposal does not load:\n%s\n%v", out, err)
	}

	if cfg.Name != "my-app" {
		t.Errorf("group name = %q, want the profile name without whitespace", cfg.Name)
	}
	if len(cfg.Services) != 4 {
		t.Fatalf("services = %d, want one per profile port", len(cfg.Services))
	}
	byName := map[string]groups.Service{}
	for _, s := range cfg.Services {
		byName[s.Name] = s
	}
	if s := byName["api"]; s.Port != 8000 || s.Health != "/healthz" {
		t.Errorf("api service = %+v, want port 8000 and health /healthz", s)
	}
	if s := byName["db"]; s.Health != "/" {
		t.Errorf("a profile health check without a path becomes %q, want /", s.Health)
	}
	if _, ok := byName["frontend-vite"]; !ok {
		t.Errorf("names are not sanitised: %v", byName)
	}
	if _, ok := byName["frontend-vite-2"]; !ok {
		t.Errorf("duplicate names are not made unique: %v", byName)
	}
	// A profile never recorded how a service starts, so nothing is invented.
	for _, s := range cfg.Services {
		if s.Cmd != "" {
			t.Errorf("service %s invented a cmd %q", s.Name, s.Cmd)
		}
	}
	if !strings.Contains(string(out), "name: my-app") {
		t.Errorf("proposal is missing the group name:\n%s", out)
	}
}

func TestProfileExportOfAnEmptyProfileStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	out, err := groups.Marshal(profileConfig(&profile.Profile{Name: "empty"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := groups.Load(path); err != nil {
		t.Fatalf("empty profile export does not load:\n%s\n%v", out, err)
	}
}
