package state

// KillMethod names what was done (or, for a dry run, what would be done) to one
// process or container. It is the `method` vocabulary of contract §3 as
// amended by §17, which splits the former "signal" into sigterm and sigkill so
// a caller can see whether escalation happened.
type KillMethod string

const (
	// MethodSIGTERM is a polite stop: SIGTERM on unix, `taskkill` without /F
	// on Windows.
	MethodSIGTERM KillMethod = "sigterm"
	// MethodSIGKILL is an unconditional stop: SIGKILL on unix, `taskkill /F`
	// on Windows. Either requested with --force or reached by escalation.
	MethodSIGKILL KillMethod = "sigkill"
	// MethodDockerStop is `docker stop <container>`.
	MethodDockerStop KillMethod = "docker_stop"
	// MethodMapStop routes a daemon-owned proxy row to map.stop (contract §3).
	// Reserved; the killer never produces it yet.
	MethodMapStop KillMethod = "map_stop"
	// MethodNone means nothing was done: the target could not be resolved, or
	// it was already gone.
	MethodNone KillMethod = "none"
)

// AllKillMethods is the enum used by schema generation.
var AllKillMethods = []KillMethod{
	MethodSIGTERM, MethodSIGKILL, MethodDockerStop, MethodMapStop, MethodNone,
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
	Method      KillMethod `json:"method" jsonschema:"enum=sigterm,enum=sigkill,enum=docker_stop,enum=map_stop,enum=none"`
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
