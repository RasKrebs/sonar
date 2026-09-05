package spawn

import (
	"strings"
	"testing"
)

func TestChildEnvCarriesTheRunIdentity(t *testing.T) {
	env := childEnv(Request{
		Env:      []string{"PATH=/bin", "SONAR_GROUP=stale", "SONAR_RUN_ID=stale"},
		Group:    "sonar",
		Name:     "api",
		PortHint: 8000,
	}, "abc123")

	want := map[string]string{
		"PATH":      "/bin",
		EnvGroup:    "sonar",
		EnvName:     "api",
		EnvRunID:    "abc123",
		EnvPortHint: "8000",
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("%s appears twice in %q", k, env)
		}
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestChildEnvOmitsThePortWhenThereIsNoHint(t *testing.T) {
	for _, kv := range childEnv(Request{Env: []string{}}, "id") {
		if strings.HasPrefix(kv, EnvPortHint+"=") {
			t.Fatalf("unexpected %s in the child environment", EnvPortHint)
		}
	}
}
