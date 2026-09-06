package state

// Snapshot is the full published state at one sequence number. Contract §5
// fixes the five collections and the top-level ExposuresActive counter; the
// remote-hosts design adds a sixth, `hosts`, which always carries at least the
// daemon's own machine.
type Snapshot struct {
	Seq             uint64          `json:"seq"`
	At              string          `json:"at"`
	DaemonVersion   string          `json:"daemon_version"`
	ExposuresActive int             `json:"exposures_active"`
	Ports           []Port          `json:"ports"`
	Groups          []Group         `json:"groups"`
	Tunnels         []Tunnel        `json:"tunnels"`
	Proxies         []Proxy         `json:"proxies"`
	Sessions        []SessionRecord `json:"sessions"`
	Hosts           []Host          `json:"hosts"`
}
