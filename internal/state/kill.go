package state

// KillAction names what was done (or, for a dry run, what would be done) to one
// process or container. It is the `action` vocabulary of the daemon spec's
// `sonar kill --json` example.
type KillAction string

const (
	// ActionSIGTERM is a polite stop: SIGTERM on unix, `taskkill` without /F
	// on Windows.
	ActionSIGTERM KillAction = "sigterm"
	// ActionSIGKILL is an unconditional stop: SIGKILL on unix, `taskkill /F`
	// on Windows. Either requested with --force or reached by escalation.
	ActionSIGKILL KillAction = "sigkill"
	// ActionDockerStop is `docker stop <container>`.
	ActionDockerStop KillAction = "docker_stop"
	// ActionMapStop routes a daemon-owned proxy row to map.stop (contract §3).
	// Reserved; the killer never produces it yet.
	ActionMapStop KillAction = "map_stop"
	// ActionNone means nothing was done: the target could not be resolved, or
	// it was already gone.
	ActionNone KillAction = "none"
)

// AllKillActions is the enum used by schema generation.
var AllKillActions = []KillAction{
	ActionSIGTERM, ActionSIGKILL, ActionDockerStop, ActionMapStop, ActionNone,
}

// KillResult is one row of a kill-shaped result (contract §3, daemon spec
// "`sonar kill`"): what happened to a single process or container. A tree kill
// emits one row per signalled process, children before parents, every row
// carrying the port of the listener whose tree it belongs to.
type KillResult struct {
	Port        int        `json:"port"`
	BindAddress string     `json:"bind_address"`
	PID         int        `json:"pid"`
	Name        string     `json:"name"`
	Action      KillAction `json:"action" jsonschema:"enum=sigterm,enum=sigkill,enum=docker_stop,enum=map_stop,enum=none"`
	OK          bool       `json:"ok"`
	Error       string     `json:"error,omitempty"`
}

// Key is the port key ("<port>:<bind_address>") this row affected. Rows for a
// target addressed by PID alone have no port and return an empty key.
func (r KillResult) Key() string {
	if r.Port == 0 {
		return ""
	}
	return Port{Port: r.Port, BindAddress: r.BindAddress}.Key()
}
