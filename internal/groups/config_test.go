package groups

import (
	"path/filepath"
	"strings"
	"testing"
)

func loadString(t *testing.T, dirName, body string) (*Config, error) {
	t.Helper()
	dir := mkdir(t, tempTree(t), dirName)
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, body)
	return Load(path)
}

func TestLoadValidConfig(t *testing.T) {
	cfg, err := loadString(t, "sonar", `
name: sonar
services:
  - name: db
    cmd: docker compose up db
    port: 5432
    health: /
  - name: api
    cmd: uv run uvicorn app:app
    cwd: backend
    port: 8000
    depends_on: [db]
ports: [9229]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Name != "sonar" {
		t.Errorf("name = %q, want sonar", cfg.Name)
	}
	if len(cfg.Services) != 2 || cfg.Services[1].Name != "api" {
		t.Fatalf("services = %+v", cfg.Services)
	}
	if got, want := cfg.ServiceDir(cfg.Services[1]), filepath.Join(cfg.Dir, "backend"); got != want {
		t.Errorf("ServiceDir = %q, want %q", got, want)
	}
	if len(cfg.Ports) != 1 || cfg.Ports[0] != 9229 {
		t.Errorf("ports = %v", cfg.Ports)
	}
}

func TestLoadDefaultsNameToDirectory(t *testing.T) {
	cfg, err := loadString(t, "storefront", "services: []\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Name != "storefront" {
		t.Errorf("name = %q, want storefront (the directory name)", cfg.Name)
	}
}

func TestLoadValidationProblems(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "name with a slash",
			body: "name: acme/web\n",
			want: `name "acme/web" contains "/"`,
		},
		{
			name: "name with whitespace",
			body: "name: my project\n",
			want: `contains " "`,
		},
		{
			name: "duplicate service names",
			body: "name: p\nservices:\n  - name: api\n  - name: api\n",
			want: "service api: duplicate name",
		},
		{
			name: "cwd escapes the config directory",
			body: "name: p\nservices:\n  - name: api\n    cwd: ../elsewhere\n",
			want: "escapes the directory",
		},
		{
			name: "absolute cwd",
			body: "name: p\nservices:\n  - name: api\n    cwd: /etc\n",
			want: "escapes the directory",
		},
		{
			name: "port out of range",
			body: "name: p\nservices:\n  - name: api\n    port: 70000\n",
			want: "port 70000 is out of range",
		},
		{
			name: "zero port in the ports list",
			body: "name: p\nports: [0]\n",
			want: "ports: 0 is out of range",
		},
		{
			name: "depends_on cycle",
			body: "name: p\nservices:\n  - name: a\n    depends_on: [b]\n  - name: b\n    depends_on: [a]\n",
			want: "depends_on has a cycle",
		},
		{
			name: "depends_on self cycle",
			body: "name: p\nservices:\n  - name: a\n    depends_on: [a]\n",
			want: "depends_on has a cycle",
		},
		{
			name: "depends_on unknown service",
			body: "name: p\nservices:\n  - name: a\n    depends_on: [ghost]\n",
			want: `depends_on "ghost" is not a service`,
		},
		{
			name: "expose at the top level",
			body: "name: p\nexpose:\n  - 3000\n",
			want: "has an `expose:` key",
		},
		{
			name: "expose on a service",
			body: "name: p\nservices:\n  - name: api\n    expose: true\n",
			want: "service api has an `expose:` key",
		},
		{
			name: "not yaml at all",
			body: "name: [unterminated\n",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadString(t, "proj", tt.body)
			if err == nil {
				t.Fatalf("Load succeeded, want an error; got %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load returned a config alongside its error: %+v", cfg)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), ConfigName) {
				t.Errorf("error %q does not name the offending file", err)
			}
		})
	}
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := loadString(t, "proj", "name: bad name\nservices:\n  - name: a\n    port: 70000\n  - name: a\n")
	if err == nil {
		t.Fatal("want an error")
	}
	ce, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("error is %T, want *ConfigError", err)
	}
	if len(ce.Problems) < 3 {
		t.Errorf("problems = %v, want all three reported at once", ce.Problems)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(tempTree(t), ConfigName)); err == nil {
		t.Fatal("want an error for a missing file")
	}
}
