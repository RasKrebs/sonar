package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/raskrebs/sonar/internal/state"
)

// contractMethods is every method the daemon must describe: spec 1's method
// table, the additions in cross-spec contract §4, and the expose/map
// namespaces spec 3 reserves.
var contractMethods = []string{
	"daemon.hello", "daemon.shutdown", "daemon.status", "daemon.schema",
	"state.snapshot", "state.subscribe", "state.unsubscribe", "stream.cancel",
	"ports.list", "ports.inspect", "ports.kill", "ports.rename", "ports.next",
	"ports.wait", "ports.health", "ports.logs", "ports.graph", "ports.history",
	"groups.list", "groups.inspect", "groups.kill", "groups.start",
	"groups.assign", "groups.reload", "groups.init",
	"runs.register", "runs.unregister", "runs.list", "runs.spawn",
	"claims.acquire", "claims.release", "claims.list",
	"sessions.list", "sessions.inspect", "sessions.kill",
	"config.get", "config.set", "config.path",
	"remote.scan",
	"expose.create", "expose.stop", "expose.list", "expose.logs",
	"expose.providers", "expose.install_provider",
	"map.create", "map.stop", "map.list", "map.requests",
}

func TestEveryContractMethodIsDescribed(t *testing.T) {
	got := Methods()
	for _, m := range contractMethods {
		if _, ok := got[m]; !ok {
			t.Errorf("method %q is in the contract but not described", m)
		}
	}
}

func TestStreamingMethodsDeclareChunkAndEnd(t *testing.T) {
	for _, m := range []string{"ports.wait", "groups.start", "map.requests", "expose.install_provider"} {
		d, ok := Methods()[m]
		if !ok {
			t.Fatalf("method %q not described", m)
		}
		if d.Chunk == nil || d.End == nil {
			t.Errorf("streaming method %q must declare chunk and end types", m)
		}
	}
}

func TestBuildSchemaHasContractDefinitions(t *testing.T) {
	b, err := json.Marshal(BuildSchema())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}

	defs, ok := m["definitions"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no definitions map")
	}
	for _, name := range []string{"Port", "Group", "Tunnel", "Proxy", "Session",
		"Claim", "Snapshot", "Delta", "Event", "Error"} {
		if _, ok := defs[name]; !ok {
			t.Errorf("definition %q missing (contract §6)", name)
		}
	}

	port, ok := defs["Port"].(map[string]any)["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Port definition has no properties")
	}
	for _, prop := range []string{"display_name", "ppid", "project_root", "group",
		"group_source", "run", "stats", "exposed_urls", "proxy_id",
		"proxy_target_port", "started_at", "session"} {
		if _, ok := port[prop]; !ok {
			t.Errorf("Port has no %q property", prop)
		}
	}

	methods, ok := m["methods"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no methods map")
	}
	for _, name := range contractMethods {
		entry, ok := methods[name].(map[string]any)
		if !ok {
			t.Errorf("methods[%q] missing from schema", name)
			continue
		}
		if _, ok := entry["params"]; !ok {
			t.Errorf("methods[%q] has no params schema", name)
		}
		if _, ok := entry["result"]; !ok {
			t.Errorf("methods[%q] has no result schema", name)
		}
	}

	if m["protocol_version"] != ProtocolVersion {
		t.Errorf("protocol_version = %v, want %s", m["protocol_version"], ProtocolVersion)
	}
}

func TestCheckedInSchemaIsUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "schema", "protocol.schema.json")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go generate ./...`)", err)
	}
	if string(onDisk) != string(Marshal()) {
		t.Fatalf("%s is stale; run `go generate ./...`", path)
	}
}

// TestSchemaEnumsMatchGoEnums guards against the Go enum constants and the
// struct-tag enums in the generated schema drifting apart.
func TestSchemaEnumsMatchGoEnums(t *testing.T) {
	var doc struct {
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(Marshal(), &doc); err != nil {
		t.Fatal(err)
	}

	wantTypes := make([]string, 0, len(state.AllPortTypes))
	for _, v := range state.AllPortTypes {
		wantTypes = append(wantTypes, string(v))
	}
	assertEnum(t, doc.Definitions["Port"].Properties["type"], wantTypes)

	wantSources := make([]string, 0, len(state.AllGroupSources))
	for _, v := range state.AllGroupSources {
		wantSources = append(wantSources, string(v))
	}
	assertEnum(t, doc.Definitions["Group"].Properties["source"], wantSources)
}

func assertEnum(t *testing.T, raw json.RawMessage, want []string) {
	t.Helper()
	var got struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	if len(got.Enum) != len(want) {
		t.Fatalf("enum = %v, want %v", got.Enum, want)
	}
	for i := range want {
		if got.Enum[i] != want[i] {
			t.Fatalf("enum = %v, want %v", got.Enum, want)
		}
	}
}
