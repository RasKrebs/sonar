package display

import (
	"encoding/json"
	"io"

	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
)

// legacyPort is the contract's state.Port plus the flat fields the pre-F0
// `--json` output carried. The nested objects (`stats`, `health`, `docker`,
// `run`) are the contract; the flat keys are kept so existing consumers — the
// macOS tray and user scripts — keep working.
//
// Deprecated: the flat fields are removed in slice F9. New consumers read the
// nested objects.
type legacyPort struct {
	state.Port

	Tag   string `json:"tag,omitempty"`
	RunID string `json:"run_id,omitempty"`

	CPUPercent  float64 `json:"cpu_percent"`
	MemoryRSS   int64   `json:"memory_rss_bytes"`
	ThreadCount int     `json:"thread_count,omitempty"`
	Uptime      string  `json:"uptime,omitempty"`
	StateName   string  `json:"state,omitempty"`
	Connections int     `json:"connections"`

	HealthStatus    string `json:"health_status,omitempty"`
	HealthCode      int    `json:"health_code,omitempty"`
	HealthLatencyMs int64  `json:"health_latency_ms,omitempty"`

	DockerContainer      string `json:"docker_container,omitempty"`
	DockerImage          string `json:"docker_image,omitempty"`
	DockerComposeService string `json:"docker_compose_service,omitempty"`
	DockerComposeProject string `json:"docker_compose_project,omitempty"`
	DockerContainerPort  int    `json:"docker_container_port,omitempty"`
}

// RenderJSON writes the ports as a JSON array of contract `state.Port` objects,
// each carrying the deprecated legacy fields alongside.
func RenderJSON(w io.Writer, pp []ports.ListeningPort) error {
	out := make([]legacyPort, len(pp))
	for i, p := range pp {
		out[i] = legacyPort{
			Port: state.FromListening(p),

			Tag:   p.Tag,
			RunID: p.RunID,

			CPUPercent:  p.CPUPercent,
			MemoryRSS:   p.MemoryRSS,
			ThreadCount: p.ThreadCount,
			Uptime:      p.Uptime,
			StateName:   p.State,
			Connections: p.Connections,

			HealthStatus:    p.HealthStatus,
			HealthCode:      p.HealthCode,
			HealthLatencyMs: p.HealthLatency.Milliseconds(),

			DockerContainer:      p.DockerContainer,
			DockerImage:          p.DockerImage,
			DockerComposeService: p.DockerComposeService,
			DockerComposeProject: p.DockerComposeProject,
			DockerContainerPort:  p.DockerContainerPort,
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
