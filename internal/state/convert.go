package state

import "github.com/raskrebs/sonar/internal/ports"

// FromListening adapts the scanner model to the published contract model. It is
// the only place that translation happens: `sonar list --json` uses it today
// and the daemon's scanner loop (slice F1) uses it too, so both paths emit
// byte-identical rows.
func FromListening(lp ports.ListeningPort) Port {
	p := Port{
		Port:        lp.Port,
		BindAddress: lp.BindAddress,
		IPVersion:   lp.IPVersion,
		URL:         lp.URL(),
		PID:         lp.PID,
		PPID:        lp.PPID,
		Process:     lp.Process,
		DisplayName: lp.DisplayName(),
		Name:        strPtr(lp.Name),
		Command:     lp.Command,
		Cwd:         lp.Cwd,
		ProjectRoot: strPtr(lp.ProjectRoot),
		Group:       strPtr(lp.Group),
		Type:        portType(lp.Type),
		IsApp:       lp.IsApp,
		User:        lp.User,
		ServiceUnit: strPtr(lp.ServiceUnit),
		ExposedURLs: []string{},
		StartedAt:   strPtr(lp.StartedAt),
	}

	if lp.GroupSource != "" {
		gs := GroupSource(lp.GroupSource)
		p.GroupSource = &gs
	}

	// Run is null for anything sonar did not start. The group comes from the
	// runs registry, which records it since `sonar start` (step 1A.5); a run
	// registered by the older `sonar run --tag` reports its tag as both.
	if lp.RunID != "" || lp.Tag != "" || lp.RunGroup != "" {
		p.Run = &Run{ID: lp.RunID, Group: lp.RunGroup, Name: lp.Tag, RootPID: lp.RunRootPID}
	}

	// Stats and health are null unless they were actually collected.
	if lp.CPUPercent != 0 || lp.MemoryRSS != 0 || lp.ThreadCount != 0 ||
		lp.Uptime != "" || lp.State != "" || lp.Connections != 0 {
		p.Stats = &Stats{
			CPUPercent:  lp.CPUPercent,
			MemoryRSS:   lp.MemoryRSS,
			ThreadCount: lp.ThreadCount,
			Uptime:      lp.Uptime,
			State:       lp.State,
			Connections: lp.Connections,
		}
	}
	if lp.HealthStatus != "" {
		status, reason := NormalizeHealth(lp.HealthStatus)
		p.Health = &Health{
			Status:    status,
			Code:      lp.HealthCode,
			LatencyMs: lp.HealthLatency.Milliseconds(),
			Reason:    reason,
		}
	}
	if lp.DockerContainer != "" || lp.DockerImage != "" || lp.DockerComposeService != "" {
		p.Docker = &Docker{
			Container:      lp.DockerContainer,
			Image:          lp.DockerImage,
			ComposeService: lp.DockerComposeService,
			ComposeProject: lp.DockerComposeProject,
			ContainerPort:  lp.DockerContainerPort,
		}
	}
	return p
}

// FromListeningAll converts a scan result to contract rows.
func FromListeningAll(pp []ports.ListeningPort) []Port {
	out := make([]Port, len(pp))
	for i := range pp {
		out[i] = FromListening(pp[i])
	}
	return out
}

// portType maps the scanner's int enum to the contract's string enum.
func portType(t ports.PortType) PortType {
	switch t {
	case ports.PortTypeSystem:
		return TypeSystem
	case ports.PortTypeDocker:
		return TypeDocker
	case ports.PortTypeProxy:
		return TypeProxy
	default:
		return TypeUser
	}
}

// strPtr returns nil for the empty string so absent values marshal as null.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
