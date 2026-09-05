package state

import (
	"time"

	"github.com/raskrebs/sonar/internal/ports"
)

// ToListening is the inverse of FromListening: it turns a published row back
// into the scanner model the renderers and the OS helpers take. The daemon's
// read handlers and every CLI command that reads through the daemon use it, so
// `sonar list` prints the same table whether the rows came off the socket or
// out of a direct scan.
//
// The round trip is lossless for everything the CLI renders. Two scanner-only
// fields have no wire representation and do not survive it: ParentCmd, whose
// only job is to help DisplayName (carried across as Display instead), and
// DockerComposeWorkingDir, which the group resolver consumes before a row is
// ever published.
func ToListening(p Port) ports.ListeningPort {
	lp := ports.ListeningPort{
		Port:        p.Port,
		PID:         p.PID,
		PPID:        p.PPID,
		Process:     p.Process,
		Display:     p.DisplayName,
		Command:     p.Command,
		Cwd:         p.Cwd,
		User:        p.User,
		BindAddress: p.BindAddress,
		IPVersion:   p.IPVersion,
		Type:        listeningType(p.Type),
		IsApp:       p.IsApp,
		Name:        derefStr(p.Name),
		ProjectRoot: derefStr(p.ProjectRoot),
		Group:       derefStr(p.Group),
		ServiceUnit: derefStr(p.ServiceUnit),
		StartedAt:   derefStr(p.StartedAt),
	}
	if p.GroupSource != nil {
		lp.GroupSource = string(*p.GroupSource)
	}
	if p.Run != nil {
		lp.RunID, lp.Tag, lp.RunRootPID = p.Run.ID, p.Run.Name, p.Run.RootPID
	}
	if p.Stats != nil {
		lp.CPUPercent = p.Stats.CPUPercent
		lp.MemoryRSS = p.Stats.MemoryRSS
		lp.ThreadCount = p.Stats.ThreadCount
		lp.Uptime = p.Stats.Uptime
		lp.State = p.Stats.State
		lp.Connections = p.Stats.Connections
	}
	if p.Health != nil {
		// The CLI's own renderers still speak the probe vocabulary, so the
		// round trip hands back the reason when there is one.
		lp.HealthStatus = p.Health.Status
		if p.Health.Reason != "" {
			lp.HealthStatus = p.Health.Reason
		}
		lp.HealthCode = p.Health.Code
		lp.HealthLatency = time.Duration(p.Health.LatencyMs) * time.Millisecond
	}
	if p.Docker != nil {
		lp.DockerContainer = p.Docker.Container
		lp.DockerImage = p.Docker.Image
		lp.DockerComposeService = p.Docker.ComposeService
		lp.DockerComposeProject = p.Docker.ComposeProject
		lp.DockerContainerPort = p.Docker.ContainerPort
	}
	return lp
}

// ToListeningAll converts a published collection back to scanner rows.
func ToListeningAll(pp []Port) []ports.ListeningPort {
	out := make([]ports.ListeningPort, len(pp))
	for i := range pp {
		out[i] = ToListening(pp[i])
	}
	return out
}

// listeningType maps the contract's string enum back to the scanner's ints.
func listeningType(t PortType) ports.PortType {
	switch t {
	case TypeSystem:
		return ports.PortTypeSystem
	case TypeDocker:
		return ports.PortTypeDocker
	case TypeProxy:
		return ports.PortTypeProxy
	default:
		return ports.PortTypeUser
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
