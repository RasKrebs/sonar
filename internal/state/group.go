package state

// Service is one entry of a group's `.sonar.yaml` services list. PortActual is
// the port the service is actually listening on right now, resolved by the
// group resolver (spec 3 needs this join).
type Service struct {
	Name       string   `json:"name"`
	Cmd        string   `json:"cmd"`
	Cwd        string   `json:"cwd"`
	Port       *int     `json:"port" jsonschema:"nullable"`
	Health     *string  `json:"health" jsonschema:"nullable"`
	DependsOn  []string `json:"depends_on"`
	Running    bool     `json:"running"`
	PortActual *int     `json:"port_actual" jsonschema:"nullable"`
}

// Group is a set of ports that belong to one project. Members are port
// numbers; clients join them against the ports list.
type Group struct {
	Name       string      `json:"name"`
	Source     GroupSource `json:"source" jsonschema:"enum=auto,enum=file,enum=manual,enum=start"`
	RootDir    *string     `json:"root_dir" jsonschema:"nullable"`
	ConfigPath *string     `json:"config_path" jsonschema:"nullable"`
	Status     string      `json:"status"` // running | partial | stopped
	Members    []int       `json:"members"`
	Services   []Service   `json:"services"`
}

// Tunnel and Proxy are owned by spec 3; Claim and SessionRecord by spec 2.
// Declared here so the snapshot has a stable shape from day one; fields follow
// those specs.
type Tunnel struct {
	ID            string  `json:"id"`
	TargetPort    int     `json:"target_port"`
	TargetGroup   *string `json:"target_group" jsonschema:"nullable"`
	TargetService *string `json:"target_service" jsonschema:"nullable"`
	Provider      string  `json:"provider"`
	Scope         string  `json:"scope"`
	PublicURL     string  `json:"public_url"`
	Name          string  `json:"name"`
	Auth          bool    `json:"auth"`
	Status        string  `json:"status"`
	StatusReason  string  `json:"status_reason"`
	Persist       bool    `json:"persist"`
	CreatedAt     string  `json:"created_at"`
	ExpiresAt     *string `json:"expires_at" jsonschema:"nullable"`
	PID           int     `json:"pid"`
	Requests      int64   `json:"requests"`
}

// Proxy is a daemon-owned TCP (or HTTP) forwarder. It also appears in the
// ports collection as a row with Type == TypeProxy.
type Proxy struct {
	ID          string `json:"id"`
	ServicePort int    `json:"service_port"`
	ListenPort  int    `json:"listen_port"`
	HTTP        bool   `json:"http"`
	Status      string `json:"status"`
	Persist     bool   `json:"persist"`
	CreatedAt   string `json:"created_at"`
	Connections int    `json:"connections"`
	Requests    int64  `json:"requests"`
}

// SessionRecord is a Session plus the aggregate counts the sessions collection
// carries.
type SessionRecord struct {
	Session
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	Runs      int    `json:"runs"`
	Ports     int    `json:"ports"`
	Groups    int    `json:"groups"`
	Active    bool   `json:"active"`
}

// Claim is owned by spec 2 (slice M5); declared here so the schema has the
// definition contract §6 requires from day one.
type Claim struct {
	Key       string `json:"key"`
	Project   string `json:"project"`
	Worktree  string `json:"worktree"`
	Ports     []int  `json:"ports"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}
