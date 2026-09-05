package groups

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commented is the file every round-trip test starts from: comments above,
// beside and inside the services list, plus a key order nobody would choose
// alphabetically.
const commented = `# The sonar repo itself.
name: sonar
services:
  # The database has to be up before anything else.
  - name: db
    cmd: docker compose up db
    port: 5432
  - name: api
    cmd: uv run api          # served by uvicorn
    cwd: backend
    port: 8000
    health: /health
    depends_on: [db]
ports: [9229]
`

func writePatchable(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestPatchServicesKeepsComments is the contract §13.2 promise: an edit made
// from the desktop app must not eat the comments the author wrote.
func TestPatchServicesKeepsComments(t *testing.T) {
	path := writePatchable(t, commented)

	cfg, err := PatchServices(path, []ServiceEdit{{
		Name: "db",
		Patch: ServicePatch{}.
			SetString(FieldHealth, "/").
			SetString(FieldColor, "blue"),
	}})
	if err != nil {
		t.Fatalf("PatchServices: %v", err)
	}

	out := read(t, path)
	for _, want := range []string{
		"# The sonar repo itself.",
		"# The database has to be up before anything else.",
		"# served by uvicorn",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("comment %q was lost:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "health: /") || !strings.Contains(out, "color: blue") {
		t.Errorf("the patch was not written:\n%s", out)
	}

	// Key order: name, cmd and port stay where they were, and the new keys are
	// inserted where a hand-written file would put them.
	db := serviceBlock(t, out, "db")
	if got := keysOf(db); !equalStrings(got, []string{"name", "cmd", "port", "health", "color"}) {
		t.Errorf("db keys = %v", got)
	}

	if cfg.Services[0].Health != "/" || cfg.Services[0].Color != "blue" {
		t.Errorf("returned config does not carry the patch: %+v", cfg.Services[0])
	}
	// The rest of the file is untouched.
	if cfg.Services[1].Cmd != "uv run api" || len(cfg.Ports) != 1 || cfg.Ports[0] != 9229 {
		t.Errorf("unrelated keys changed: %+v", cfg)
	}
}

// TestPatchServicesNullClears covers the null half of ServicePatch: a key set
// to null disappears from the file rather than being written as an empty
// string.
func TestPatchServicesNullClears(t *testing.T) {
	path := writePatchable(t, commented)

	if _, err := PatchServices(path, []ServiceEdit{{
		Name:  "api",
		Patch: ServicePatch{}.Clear(FieldHealth),
	}}); err != nil {
		t.Fatalf("PatchServices: %v", err)
	}

	out := read(t, path)
	if strings.Contains(out, "health:") {
		t.Errorf("health was not cleared:\n%s", out)
	}
	if !strings.Contains(out, "cwd: backend") || !strings.Contains(out, "depends_on:") {
		t.Errorf("clearing one key disturbed its neighbours:\n%s", out)
	}
}

// TestPatchServicesFromJSON is the wire path: absent keys are left alone and an
// explicit null clears, which pointers alone cannot tell apart.
func TestPatchServicesFromJSON(t *testing.T) {
	var patch ServicePatch
	if err := json.Unmarshal([]byte(`{"description": "The API", "health": null}`), &patch); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(patch.Sent, []string{"description", "health"}) {
		t.Fatalf("sent keys = %v", patch.Sent)
	}

	path := writePatchable(t, commented)
	cfg, err := PatchServices(path, []ServiceEdit{{Name: "api", Patch: patch}})
	if err != nil {
		t.Fatalf("PatchServices: %v", err)
	}
	if cfg.Services[1].Description != "The API" {
		t.Errorf("description not written: %+v", cfg.Services[1])
	}
	if cfg.Services[1].Health != "" {
		t.Errorf("null did not clear health: %+v", cfg.Services[1])
	}
	if cfg.Services[1].Cwd != "backend" || cfg.Services[1].Port != 8000 {
		t.Errorf("untouched keys were rewritten: %+v", cfg.Services[1])
	}
}

// TestPatchServicesUnknownService leaves the file alone and reports the name,
// which the daemon turns into `not_found`.
func TestPatchServicesUnknownService(t *testing.T) {
	path := writePatchable(t, commented)
	before := read(t, path)

	_, err := PatchServices(path, []ServiceEdit{{
		Name:  "worker",
		Patch: ServicePatch{}.SetString(FieldIcon, "gear"),
	}})
	var missing *ServiceNotFoundError
	if !errors.As(err, &missing) || missing.Name != "worker" {
		t.Fatalf("expected a ServiceNotFoundError for worker, got %v", err)
	}
	if read(t, path) != before {
		t.Error("the file was rewritten despite the error")
	}
}

// TestPatchServicesInvalidResultIsNotWritten: a patch that breaks validation
// comes back as a *ConfigError and the file on disk is untouched, so an
// out-of-range port from a client cannot leave a project unloadable.
func TestPatchServicesInvalidResultIsNotWritten(t *testing.T) {
	path := writePatchable(t, commented)
	before := read(t, path)

	_, err := PatchServices(path, []ServiceEdit{{
		Name:  "api",
		Patch: ServicePatch{}.SetPort(70000),
	}})
	var bad *ConfigError
	if !errors.As(err, &bad) {
		t.Fatalf("expected a *ConfigError, got %v", err)
	}
	if len(bad.Problems) == 0 || !strings.Contains(bad.Error(), "70000") {
		t.Errorf("the error should name the bad port: %v", bad)
	}
	if read(t, path) != before {
		t.Error("an invalid patch was written to disk")
	}
}

// TestPatchServicesQuotesWhenItMust: a colour like "#3b82f6" is a comment in
// plain YAML, so the writer has to quote it and the file has to re-read.
func TestPatchServicesQuotesWhenItMust(t *testing.T) {
	path := writePatchable(t, commented)

	cfg, err := PatchServices(path, []ServiceEdit{{
		Name: "db",
		Patch: ServicePatch{}.
			SetString(FieldColor, "#3b82f6").
			SetString(FieldDescription, "Postgres: the one with the data"),
	}})
	if err != nil {
		t.Fatalf("PatchServices: %v", err)
	}
	if cfg.Services[0].Color != "#3b82f6" {
		t.Fatalf("colour did not survive: %q", cfg.Services[0].Color)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatalf("reloading the written file: %v", err)
	}
	if again.Services[0].Color != "#3b82f6" || again.Services[0].Description != "Postgres: the one with the data" {
		t.Fatalf("re-read config lost a value: %+v", again.Services[0])
	}
}

// TestPatchServicesRepeatedEdits keeps a second edit from duplicating a key.
func TestPatchServicesRepeatedEdits(t *testing.T) {
	path := writePatchable(t, commented)

	for _, icon := range []string{"database", "server"} {
		if _, err := PatchServices(path, []ServiceEdit{{
			Name:  "db",
			Patch: ServicePatch{}.SetString(FieldIcon, icon),
		}}); err != nil {
			t.Fatalf("PatchServices(%s): %v", icon, err)
		}
	}
	out := read(t, path)
	if n := strings.Count(out, "icon:"); n != 1 {
		t.Fatalf("icon written %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "icon: server") {
		t.Fatalf("the second edit did not win:\n%s", out)
	}
}

// serviceBlock returns the lines of one service item from a rendered config.
func serviceBlock(t *testing.T, yaml, name string) []string {
	t.Helper()
	var block []string
	in := false
	for _, line := range strings.Split(yaml, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			in = trimmed == "- name: "+name
			if in {
				block = append(block, strings.TrimPrefix(trimmed, "- "))
			}
			continue
		}
		if in {
			if !strings.HasPrefix(line, "    ") || trimmed == "" {
				break
			}
			block = append(block, trimmed)
		}
	}
	if len(block) == 0 {
		t.Fatalf("no service %q in:\n%s", name, yaml)
	}
	return block
}

func keysOf(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, ok := strings.Cut(line, ":"); ok {
			out = append(out, strings.TrimSpace(key))
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
