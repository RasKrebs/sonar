package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/ports"
)

// fullRow is a scan row with every field the wire shape carries set, so the
// round-trip test fails loudly when a field is added to one side only.
func fullRow() ports.ListeningPort {
	return ports.ListeningPort{
		Port:        8000,
		PID:         42,
		PPID:        7,
		Process:     "python3",
		Command:     "python3 -m uvicorn app:app",
		ParentCmd:   "npm run dev",
		Cwd:         "/home/dev/api",
		ServiceUnit: "api.service",
		User:        "dev",
		BindAddress: "127.0.0.1",
		IPVersion:   "IPv4",
		Type:        ports.PortTypeUser,
		IsApp:       false,
		Tag:         "web",
		RunID:       "run-1",
		RunRootPID:  40,
		Name:        "api",
		ProjectRoot: "/home/dev/api",
		Group:       "api",
		GroupSource: "file",
		StartedAt:   "2026-09-05T10:00:00Z",

		CPUPercent:  12.5,
		MemoryRSS:   1024,
		ThreadCount: 8,
		Uptime:      "3h",
		State:       "sleeping",
		Connections: 3,

		HealthStatus:  "healthy",
		HealthCode:    200,
		HealthLatency: 12 * time.Millisecond,

		DockerContainer:      "api-1",
		DockerImage:          "api:latest",
		DockerComposeService: "api",
		DockerComposeProject: "shop",
		DockerContainerPort:  8000,
	}
}

// TestRoundTripPreservesTheWireShape is the unit-level half of spec
// integration test 6: a row published by the daemon and converted back renders
// from the same values a direct scan would render from.
func TestRoundTripPreservesTheWireShape(t *testing.T) {
	row := fullRow()
	published := FromListening(row)
	again := FromListening(ToListening(published))

	if !reflect.DeepEqual(published, again) {
		t.Fatalf("round trip changed the published row:\n before %+v\n after  %+v", published, again)
	}
}

// TestRoundTripKeepsTheDisplayName guards the one derived value the wire shape
// cannot recompute: DisplayName needs the parent cmdline, which is not
// published, so the converter carries the resolved name across instead.
func TestRoundTripKeepsTheDisplayName(t *testing.T) {
	row := ports.ListeningPort{
		Port:      3000,
		Process:   "node",
		Command:   "node /home/dev/site/node_modules/.bin/next dev",
		ParentCmd: "npm run dev",
		Cwd:       "/home/dev/site",
	}
	want := row.DisplayName()

	back := ToListening(FromListening(row))
	if back.ParentCmd != "" {
		t.Fatal("the parent cmdline is not published and must not reappear")
	}
	if got := back.DisplayName(); got != want {
		t.Fatalf("display name after the round trip = %q, want %q", got, want)
	}
}

func TestToListeningMapsTypesBothWays(t *testing.T) {
	for _, tt := range []struct {
		in   ports.PortType
		wire PortType
	}{
		{ports.PortTypeSystem, TypeSystem},
		{ports.PortTypeUser, TypeUser},
		{ports.PortTypeDocker, TypeDocker},
		{ports.PortTypeProxy, TypeProxy},
	} {
		row := ports.ListeningPort{Port: 1, Type: tt.in}
		p := FromListening(row)
		if p.Type != tt.wire {
			t.Fatalf("FromListening(%v).Type = %q, want %q", tt.in, p.Type, tt.wire)
		}
		if got := ToListening(p).Type; got != tt.in {
			t.Fatalf("ToListening(%q).Type = %v, want %v", tt.wire, got, tt.in)
		}
	}
}

func TestToListeningLeavesAbsentEnrichmentsZero(t *testing.T) {
	p := FromListening(ports.ListeningPort{Port: 5432, Process: "postgres"})
	if p.Stats != nil || p.Health != nil || p.Docker != nil || p.Run != nil {
		t.Fatalf("a bare row should publish null enrichments, got %+v", p)
	}
	back := ToListening(p)
	if back.CPUPercent != 0 || back.HealthStatus != "" || back.DockerContainer != "" || back.RunID != "" {
		t.Fatalf("null enrichments should convert back to zero values, got %+v", back)
	}
}
