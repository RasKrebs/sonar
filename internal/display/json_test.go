package display

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
)

func TestRenderJSONEmitsContractAndLegacyFields(t *testing.T) {
	var buf bytes.Buffer
	err := RenderJSON(&buf, []ports.ListeningPort{{
		Port: 3000, BindAddress: "127.0.0.1", PID: 42, Process: "node",
		Command: "node server.js", Type: ports.PortTypeUser,
		CPUPercent: 2.5, MemoryRSS: 1024, Connections: 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	row := rows[0]
	for _, k := range []string{"display_name", "ppid", "project_root", "group",
		"group_source", "stats", "run", "exposed_urls", "started_at", "name",
		"is_app", "service_unit", "session", "proxy_id", "proxy_target_port"} {
		if _, ok := row[k]; !ok {
			t.Errorf("contract key %q missing", k)
		}
	}
	for _, k := range []string{"cpu_percent", "memory_rss_bytes", "connections",
		"type", "url", "port", "pid", "process", "command", "bind_address"} {
		if _, ok := row[k]; !ok {
			t.Errorf("legacy key %q missing — the tray depends on it", k)
		}
	}
	if row["cpu_percent"].(float64) != 2.5 {
		t.Fatalf("cpu_percent = %v", row["cpu_percent"])
	}
	if row["type"] != "user" {
		t.Fatalf("type = %v", row["type"])
	}
	if row["url"] != "http://127.0.0.1:3000" {
		t.Fatalf("url = %v", row["url"])
	}
	// Stats were collected, so the nested object must be present too.
	stats, ok := row["stats"].(map[string]any)
	if !ok {
		t.Fatalf("stats = %v, want object", row["stats"])
	}
	if stats["cpu_percent"].(float64) != 2.5 {
		t.Fatalf("stats.cpu_percent = %v", stats["cpu_percent"])
	}
}

func TestRenderJSONNullStatsWhenNotCollected(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, []ports.ListeningPort{{
		Port: 8080, BindAddress: "0.0.0.0", PID: 7, Process: "python3",
	}}); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["stats"] != nil {
		t.Fatalf("stats = %v, want null", rows[0]["stats"])
	}
	if rows[0]["health"] != nil {
		t.Fatalf("health = %v, want null", rows[0]["health"])
	}
}

func TestRenderJSONDockerAndHealthRow(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, []ports.ListeningPort{{
		Port: 5432, BindAddress: "0.0.0.0", PID: 9, Process: "com.docke",
		Type:                 ports.PortTypeDocker,
		DockerContainer:      "sonar-db-1",
		DockerImage:          "postgres:16",
		DockerComposeService: "db",
		DockerComposeProject: "sonar",
		DockerContainerPort:  5432,
		HealthStatus:         "ok", HealthCode: 200, HealthLatency: 4 * time.Millisecond,
	}}); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row["docker_compose_service"] != "db" {
		t.Fatalf("legacy docker_compose_service = %v", row["docker_compose_service"])
	}
	d, ok := row["docker"].(map[string]any)
	if !ok || d["compose_service"] != "db" {
		t.Fatalf("docker = %v", row["docker"])
	}
	if row["display_name"] != "db" {
		t.Fatalf("display_name = %v, want db", row["display_name"])
	}
	h, ok := row["health"].(map[string]any)
	if !ok || h["code"].(float64) != 200 {
		t.Fatalf("health = %v", row["health"])
	}
}

func TestRenderJSONEmptyListIsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Fatalf("got %q, want []", got)
	}
}
