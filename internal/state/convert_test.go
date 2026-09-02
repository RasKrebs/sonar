package state

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
)

func TestFromListeningMapsContractFields(t *testing.T) {
	lp := ports.ListeningPort{
		Port: 5173, BindAddress: "127.0.0.1", IPVersion: "IPv4",
		PID: 48211, PPID: 48190, Process: "node",
		Command:     "node /Users/me/code/sonar/frontend/node_modules/.bin/vite",
		Cwd:         "/Users/me/code/sonar/frontend",
		ProjectRoot: "/Users/me/code/sonar", Group: "sonar", GroupSource: "auto",
		User: "me", Type: ports.PortTypeUser,
		Tag: "frontend", RunID: "a1b2c3d4", RunRootPID: 48190,
		StartedAt: "2026-09-02T09:12:41+02:00",
	}
	p := FromListening(lp)

	if p.PPID != 48190 || p.URL != "http://127.0.0.1:5173" {
		t.Fatalf("%+v", p)
	}
	if p.DisplayName == "" {
		t.Fatalf("display_name must be computed")
	}
	if p.ProjectRoot == nil || *p.ProjectRoot != "/Users/me/code/sonar" {
		t.Fatalf("project_root = %v", p.ProjectRoot)
	}
	if p.Group == nil || *p.Group != "sonar" {
		t.Fatalf("group = %v", p.Group)
	}
	if p.GroupSource == nil || *p.GroupSource != SourceAuto {
		t.Fatalf("group_source = %v", p.GroupSource)
	}
	if p.Run == nil || p.Run.ID != "a1b2c3d4" || p.Run.Name != "frontend" || p.Run.RootPID != 48190 {
		t.Fatalf("run = %+v", p.Run)
	}
	if p.StartedAt == nil || *p.StartedAt != "2026-09-02T09:12:41+02:00" {
		t.Fatalf("started_at = %v", p.StartedAt)
	}
	if p.Type != TypeUser {
		t.Fatalf("type = %q", p.Type)
	}
	if p.Stats != nil || p.Health != nil || p.Docker != nil {
		t.Fatalf("uncollected sub-objects must be nil")
	}
	if p.Name != nil {
		t.Fatalf("name must be null in F0")
	}
}

func TestFromListeningStatsHealthDockerWhenCollected(t *testing.T) {
	lp := ports.ListeningPort{
		Port: 5432, BindAddress: "0.0.0.0", Type: ports.PortTypeDocker,
		CPUPercent: 1.2, MemoryRSS: 183500800, ThreadCount: 14,
		Uptime: "2h13m", State: "sleeping", Connections: 2,
		HealthStatus: "ok", HealthCode: 200, HealthLatency: 4 * time.Millisecond,
		DockerContainer: "db", DockerImage: "postgres:16",
		DockerComposeService: "db", DockerComposeProject: "sonar", DockerContainerPort: 5432,
	}
	p := FromListening(lp)
	if p.Stats == nil || p.Stats.CPUPercent != 1.2 || p.Stats.Connections != 2 {
		t.Fatalf("stats = %+v", p.Stats)
	}
	if p.Health == nil || p.Health.Code != 200 || p.Health.LatencyMs != 4 {
		t.Fatalf("health = %+v", p.Health)
	}
	if p.Docker == nil || p.Docker.ComposeService != "db" {
		t.Fatalf("docker = %+v", p.Docker)
	}
	if p.Type != TypeDocker {
		t.Fatalf("type = %q", p.Type)
	}
}

func TestFromListeningProxyType(t *testing.T) {
	p := FromListening(ports.ListeningPort{Port: 3002, Type: ports.PortTypeProxy})
	if p.Type != TypeProxy {
		t.Fatalf("type = %q, want proxy", p.Type)
	}
}

func TestFromListeningJSONHasNullStatsWhenNotCollected(t *testing.T) {
	b, err := json.Marshal(FromListening(ports.ListeningPort{Port: 3000, BindAddress: "::1"}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["stats"] != nil {
		t.Fatalf("stats = %v, want null", m["stats"])
	}
	if _, ok := m["display_name"]; !ok {
		t.Fatalf("display_name missing")
	}
	if _, ok := m["ppid"]; !ok {
		t.Fatalf("ppid missing")
	}
	if urls, ok := m["exposed_urls"].([]any); !ok || urls == nil {
		t.Fatalf("exposed_urls = %v, want []", m["exposed_urls"])
	}
}
