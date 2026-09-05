package state

import (
	"encoding/json"
	"testing"
)

func TestPortKey(t *testing.T) {
	p := Port{Port: 3000, BindAddress: "127.0.0.1"}
	if got := p.Key(); got != "3000:127.0.0.1" {
		t.Fatalf("Key() = %q", got)
	}
}

func TestPortJSONNullsForAbsentOptionals(t *testing.T) {
	b, err := json.Marshal(Port{Port: 3000, BindAddress: "::", Type: TypeUser, ExposedURLs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "project_root", "group", "group_source", "session",
		"service_unit", "run", "stats", "health", "docker", "proxy_id",
		"proxy_target_port", "started_at"} {
		v, ok := m[k]
		if !ok {
			t.Fatalf("key %q missing from Port JSON", k)
		}
		if v != nil {
			t.Fatalf("key %q = %v, want null", k, v)
		}
	}
	if m["exposed_urls"] == nil {
		t.Fatalf("exposed_urls must marshal as [], not null")
	}
	if m["type"] != "user" {
		t.Fatalf("type = %v", m["type"])
	}
}

// TestNormalizeHealth pins the one published vocabulary: the desktop branches
// on ok/fail/unknown and nothing else, while the probe's own word survives as
// the reason (step 1A.7).
func TestNormalizeHealth(t *testing.T) {
	cases := []struct {
		raw          string
		want, reason string
	}{
		{"healthy", HealthOK, "healthy"},
		{"ok", HealthOK, "ok"},
		{"unhealthy", HealthFail, "unhealthy"},
		{"refused", HealthFail, "refused"},
		{"timeout", HealthFail, "timeout"},
		{"non-http", HealthFail, "non-http"},
		{"fail", HealthFail, ""},
		{"unknown", HealthUnknown, ""},
		{"", HealthUnknown, ""},
	}
	for _, c := range cases {
		got, reason := NormalizeHealth(c.raw)
		if got != c.want || reason != c.reason {
			t.Errorf("NormalizeHealth(%q) = %q, %q; want %q, %q", c.raw, got, reason, c.want, c.reason)
		}
	}
}
