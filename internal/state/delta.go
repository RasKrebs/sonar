package state

// Change is one collection's diff. Added and Updated carry full objects;
// Removed carries keys (Port.Key() for ports, Name for groups and hosts, ID
// for the rest). All three always marshal as arrays, never null.
type Change[T any] struct {
	Added   []T      `json:"added"`
	Updated []T      `json:"updated"`
	Removed []string `json:"removed"`
}

// Delta is the incremental update broadcast to state.subscribe subscribers.
type Delta struct {
	Seq             uint64                `json:"seq"`
	At              string                `json:"at"`
	ExposuresActive int                   `json:"exposures_active"`
	Ports           Change[Port]          `json:"ports"`
	Groups          Change[Group]         `json:"groups"`
	Tunnels         Change[Tunnel]        `json:"tunnels"`
	Proxies         Change[Proxy]         `json:"proxies"`
	Sessions        Change[SessionRecord] `json:"sessions"`
	Hosts           Change[Host]          `json:"hosts"`
}

// Event is a discrete notification, sent alongside deltas when a subscriber
// asked for events.
type Event struct {
	// Host is the machine the event happened on: "localhost", or the
	// registered name of the remote host whose bridge forwarded it.
	Host  string         `json:"host,omitempty"`
	Kind  string         `json:"kind"` // port_up port_down port_restarted group_up group_down health_changed scan_error daemon_stopping db_reset tunnel_up tunnel_down
	At    string         `json:"at"`
	Port  *Port          `json:"port,omitempty"`
	Group *string        `json:"group,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}
