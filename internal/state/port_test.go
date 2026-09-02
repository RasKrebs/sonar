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
