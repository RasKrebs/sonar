package rpc

import (
	"encoding/json"

	"github.com/raskrebs/sonar/internal/state"
)

// This file declares the params and results of every daemon method named in
// the daemon spec's method table and in cross-spec contract §4. In slice F0
// they are placeholders: they fix the wire shape and drive schema generation,
// and the slices that implement each method fill in the behaviour behind them.
// Changing a field here changes the published protocol, so keep it in step
// with the contract.

// ---------------------------------------------------------------- shared ---

// Selector addresses one port (contract §3). Exactly one of Port, PID, RunID
// and ProxyID is set; BindAddress only disambiguates Port.
type Selector struct {
	Port        *int    `json:"port,omitempty"`
	PID         *int    `json:"pid,omitempty"`
	BindAddress *string `json:"bind_address,omitempty"`
	RunID       *string `json:"run_id,omitempty"`
	ProxyID     *string `json:"proxy_id,omitempty"`
}

// MutationResult is the minimum every mutating method returns (contract §3).
// Affected holds port keys ("<port>:<bind_address>").
type MutationResult struct {
	OK       bool     `json:"ok"`
	Affected []string `json:"affected"`
}

// KillEnvelope is the result of every kill-shaped method (contract §3). The row
// type is state.KillResult, the single Go type the killer, the CLI and the
// daemon all produce, so it keeps the plain name in the generated schema
// (contract §17) and the envelope around it is named for what it is.
type KillEnvelope struct {
	MutationResult
	Results []state.KillResult `json:"results"`
}

// Empty is the params or result of a method that takes or returns nothing.
type Empty struct{}

// OKResult is a bare acknowledgement.
type OKResult struct {
	OK bool `json:"ok"`
}

// Include lists the optional per-subscriber enrichments ("stats", "health").
type Include []string

// ---------------------------------------------------------------- daemon ---

type DaemonHelloParams struct {
	Client        string `json:"client"` // cli | app | mcp | tray
	ClientVersion string `json:"client_version"`
	Keepalive     bool   `json:"keepalive,omitempty"`
}

type DaemonHelloResult struct {
	ProtocolVersion string   `json:"protocol_version"`
	DaemonVersion   string   `json:"daemon_version"`
	PID             int      `json:"pid"`
	StartedAt       string   `json:"started_at"`
	Capabilities    []string `json:"capabilities"`
	Socket          string   `json:"socket"`
	BinaryPath      string   `json:"binary_path"`
	Keepalive       bool     `json:"keepalive"`
}

type DaemonStatusResult struct {
	PID            int    `json:"pid"`
	Uptime         string `json:"uptime"`
	Subscribers    int    `json:"subscribers"`
	LastScanAt     string `json:"last_scan_at"`
	ScanIntervalMs int    `json:"scan_interval_ms"`
	DBPath         string `json:"db_path"`
}

// DaemonSchemaResult is the JSON Schema bundle this package generates.
type DaemonSchemaResult struct {
	Schema json.RawMessage `json:"schema"`
}

// ----------------------------------------------------------------- state ---

type StateSnapshotParams struct {
	Include Include `json:"include,omitempty"`
}

type StateSubscribeParams struct {
	Include Include `json:"include,omitempty"`
	Events  bool    `json:"events,omitempty"`
}

// ----------------------------------------------------------------- ports ---

type PortsListParams struct {
	Group     *string `json:"group,omitempty"`
	Filter    *string `json:"filter,omitempty"` // docker | user | system
	All       bool    `json:"all,omitempty"`
	IPVersion *string `json:"ip_version,omitempty"`
	Include   Include `json:"include,omitempty"`
}

type PortsListResult struct {
	Ports []state.Port `json:"ports"`
}

type PortsInspectResult struct {
	Port        state.Port   `json:"port"`
	LogSources  []string     `json:"log_sources"`
	Connections []Connection `json:"connections"`
}

// Connection is one established peer of a listening socket.
type Connection struct {
	RemotePort int `json:"remote_port"`
	RemotePID  int `json:"remote_pid"`
}

type PortsKillParams struct {
	Targets  []Selector `json:"targets"`
	Tree     bool       `json:"tree,omitempty"`
	Force    bool       `json:"force,omitempty"`
	GraceMs  int        `json:"grace_ms,omitempty"`
	Escalate *bool      `json:"escalate,omitempty"`
	DryRun   bool       `json:"dry_run,omitempty"`
}

type PortsRenameParams struct {
	Selector
	Name *string `json:"name" jsonschema:"nullable"`
}

type PortsRenameResult struct {
	MutationResult
	Key  string  `json:"key"`
	Name *string `json:"name" jsonschema:"nullable"`
}

type PortsNextParams struct {
	Start    int     `json:"start,omitempty"`
	End      int     `json:"end,omitempty"`
	Count    int     `json:"count,omitempty"`
	ClaimKey *string `json:"claim_key,omitempty"`
}

type PortsNextResult struct {
	Ports []int `json:"ports"`
}

type PortsWaitParams struct {
	Ports      []int   `json:"ports,omitempty"`
	RunID      *string `json:"run_id,omitempty"`
	Any        bool    `json:"any,omitempty"`
	HTTP       *string `json:"http,omitempty"`
	TimeoutMs  int     `json:"timeout_ms"`
	IntervalMs int     `json:"interval_ms,omitempty"`
}

type PortsWaitChunk struct {
	Port    int    `json:"port"`
	ReadyAt string `json:"ready_at"`
}

type PortsWaitEnd struct {
	Ready    []int `json:"ready"`
	TimedOut []int `json:"timed_out"`
}

type PortsHealthParams struct {
	Ports []int `json:"ports,omitempty"`
}

type PortsHealthResult struct {
	Results []PortHealth `json:"results"`
}

type PortHealth struct {
	Port      int    `json:"port"`
	Status    string `json:"status"`
	Code      int    `json:"code"`
	LatencyMs int64  `json:"latency_ms"`
}

type PortsLogsParams struct {
	Selector Selector `json:"selector"`
	Lines    int      `json:"lines,omitempty"`
	Follow   bool     `json:"follow,omitempty"`
}

// PortsLogsResult is the unary reply (follow: false). With follow: true the
// method also returns a subscription_id and pushes PortsLogsChunk.
type PortsLogsResult struct {
	Source         string   `json:"source"`
	Lines          []string `json:"lines"`
	Truncated      bool     `json:"truncated"`
	SubscriptionID string   `json:"subscription_id,omitempty"`
}

type PortsLogsChunk struct {
	Source string `json:"source"`
	Line   string `json:"line"`
}

type PortsGraphResult struct {
	Connections []GraphEdge `json:"connections"`
}

type GraphEdge struct {
	FromPort int `json:"from_port"`
	FromPID  int `json:"from_pid"`
	ToPort   int `json:"to_port"`
	ToPID    int `json:"to_pid"`
}

type PortsHistoryParams struct {
	Port  *int    `json:"port,omitempty"`
	Since *string `json:"since,omitempty"`
	Limit int     `json:"limit,omitempty"`
}

type PortsHistoryResult struct {
	Events []HistoryEvent `json:"events"`
}

type HistoryEvent struct {
	At          string `json:"at"`
	Kind        string `json:"kind"`
	Port        int    `json:"port"`
	PID         int    `json:"pid"`
	DisplayName string `json:"display_name"`
	Group       string `json:"group"`
}

// ---------------------------------------------------------------- groups ---

type GroupsListResult struct {
	Groups []state.Group `json:"groups"`
}

type GroupsInspectParams struct {
	Name string `json:"name"`
}

type GroupsInspectResult struct {
	state.Group
	Ports []state.Port `json:"ports"`
}

type GroupsKillParams struct {
	Name    string `json:"name"`
	Force   bool   `json:"force,omitempty"`
	GraceMs int    `json:"grace_ms,omitempty"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

type GroupsStartParams struct {
	Name             *string  `json:"name,omitempty"`
	ConfigPath       *string  `json:"config_path,omitempty"`
	Only             []string `json:"only,omitempty"`
	AllowOutsideHome bool     `json:"allow_outside_home,omitempty"`
}

type GroupsStartResult struct {
	MutationResult
	SubscriptionID string `json:"subscription_id"`
}

type GroupsStartChunk struct {
	Service string `json:"service"`
	PID     int    `json:"pid,omitempty"`
	LogPath string `json:"log_path,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

type GroupsStartEnd struct {
	Started []string `json:"started"`
	Skipped []string `json:"skipped"`
	Failed  []string `json:"failed"`
}

type GroupsAssignParams struct {
	Selector
	Group *string `json:"group" jsonschema:"nullable"`
}

type GroupsAssignResult struct {
	MutationResult
	Key   string  `json:"key"`
	Group *string `json:"group" jsonschema:"nullable"`
}

type GroupsReloadResult struct {
	Loaded int      `json:"loaded"`
	Errors []string `json:"errors"`
}

type GroupsInitParams struct {
	RootDir string `json:"root_dir"`
	Write   bool   `json:"write,omitempty"`
}

type GroupsInitResult struct {
	MutationResult
	Path     string      `json:"path"`
	YAML     string      `json:"yaml"`
	Proposal state.Group `json:"proposal"`
}

// ------------------------------------------------------------------ runs ---

type RunsRegisterParams struct {
	PID       int     `json:"pid"`
	PPID      int     `json:"ppid"`
	Group     string  `json:"group"`
	Name      string  `json:"name"`
	Cmd       string  `json:"cmd"`
	Cwd       string  `json:"cwd"`
	PortHint  *int    `json:"port_hint,omitempty"`
	StartedAt string  `json:"started_at"`
	ID        *string `json:"id,omitempty"`
	// AllowOutsideHome opts out of the daemon's refusal to record a run whose
	// cwd is outside the user's home (daemon spec, "Transport details"). The
	// CLI sets it; the MCP server does not.
	AllowOutsideHome bool `json:"allow_outside_home,omitempty"`
}

type RunsRegisterResult struct {
	ID string `json:"id"`
}

type RunsUnregisterParams struct {
	PID int `json:"pid"`
}

type RunsListResult struct {
	Runs []RunRecord `json:"runs"`
}

type RunRecord struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Group     string `json:"group"`
	Name      string `json:"name"`
	Cmd       string `json:"cmd"`
	Cwd       string `json:"cwd"`
	StartedAt string `json:"started_at"`
	Ports     []int  `json:"ports"`
	// PortHint is the port `sonar start --port` said this run would bind.
	PortHint *int `json:"port_hint,omitempty"`
	// Status is "starting" while a run with a port hint has not bound it yet,
	// and "running" otherwise.
	Status string `json:"status"`
}

type RunsSpawnParams struct {
	Argv             []string          `json:"argv"`
	Cwd              string            `json:"cwd"`
	Env              map[string]string `json:"env,omitempty"`
	Group            *string           `json:"group,omitempty"`
	Name             *string           `json:"name,omitempty"`
	PortHint         *int              `json:"port_hint,omitempty"`
	Session          *string           `json:"session,omitempty"`
	AllowOutsideHome bool              `json:"allow_outside_home,omitempty"`
}

type RunsSpawnResult struct {
	MutationResult
	RunID   string `json:"run_id"`
	PID     int    `json:"pid"`
	LogPath string `json:"log_path"`
}

// ---------------------------------------------------------------- claims ---

type ClaimsAcquireParams struct {
	Project  string `json:"project"`
	Worktree string `json:"worktree,omitempty"`
	Count    int    `json:"count,omitempty"`
	TTLMs    int64  `json:"ttl_ms,omitempty"`
}

type ClaimsAcquireResult struct {
	MutationResult
	Key       string `json:"key"`
	Ports     []int  `json:"ports"`
	ExpiresAt string `json:"expires_at"`
}

type ClaimsReleaseParams struct {
	Key string `json:"key"`
}

type ClaimsReleaseResult struct {
	OK       bool `json:"ok"`
	Released int  `json:"released"`
}

type ClaimsListResult struct {
	Claims []state.Claim `json:"claims"`
}

// -------------------------------------------------------------- sessions ---

type SessionsListParams struct {
	ActiveOnly bool `json:"active_only,omitempty"`
}

type SessionsListResult struct {
	Sessions []state.SessionRecord `json:"sessions"`
}

type SessionsInspectParams struct {
	ID string `json:"id"`
}

type SessionsInspectResult struct {
	Session state.SessionRecord `json:"session"`
	Runs    []RunRecord         `json:"runs"`
	Ports   []state.Port        `json:"ports"`
}

type SessionsKillParams struct {
	ID     string `json:"id"`
	Tree   bool   `json:"tree,omitempty"`
	Force  bool   `json:"force,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// ---------------------------------------------------------------- config ---

type ConfigGetResult struct {
	Config map[string]any `json:"config"`
}

type ConfigSetParams struct {
	Patch map[string]any `json:"patch"`
}

type ConfigSetResult struct {
	OK     bool           `json:"ok"`
	Config map[string]any `json:"config"`
}

type ConfigPathResult struct {
	Path string `json:"path"`
}

// ---------------------------------------------------------------- remote ---

type RemoteScanParams struct {
	Host string `json:"host"`
}

type RemoteScanResult struct {
	Ports []state.Port `json:"ports"`
}

// ---------------------------------------------------------------- expose ---

type ExposeCreateParams struct {
	Target   string  `json:"target"`
	Provider *string `json:"provider,omitempty"`
	Name     *string `json:"name,omitempty"`
	Auth     *string `json:"auth,omitempty"`
	TTL      *string `json:"ttl,omitempty"`
	Persist  bool    `json:"persist,omitempty"`
	Scope    *string `json:"scope,omitempty"`
}

type ExposeCreateResult struct {
	MutationResult
	Tunnel state.Tunnel `json:"tunnel"`
}

type ExposeStopParams struct {
	ID   *string `json:"id,omitempty"`
	Port *int    `json:"port,omitempty"`
	All  bool    `json:"all,omitempty"`
}

type ExposeStopResult struct {
	MutationResult
	Stopped []string `json:"stopped"`
}

type ExposeListResult struct {
	Tunnels []state.Tunnel `json:"tunnels"`
}

type ExposeLogsParams struct {
	ID   string `json:"id"`
	Tail int    `json:"tail,omitempty"`
}

type ExposeLogsResult struct {
	Lines []string `json:"lines"`
}

type ExposeProvidersResult struct {
	Providers []ProviderInfo `json:"providers"`
}

type ProviderInfo struct {
	Name         string   `json:"name"`
	Available    string   `json:"available"`
	Capabilities []string `json:"capabilities"`
	Hint         string   `json:"hint,omitempty"`
}

type ExposeInstallProviderParams struct {
	Provider string `json:"provider"`
	Confirm  bool   `json:"confirm"`
}

type ExposeInstallProviderResult struct {
	MutationResult
	SubscriptionID string `json:"subscription_id"`
}

type ExposeInstallProviderChunk struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

type ExposeInstallProviderEnd struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// ------------------------------------------------------------------- map ---

type MapCreateParams struct {
	ServicePort int  `json:"service_port"`
	ListenPort  int  `json:"listen_port"`
	HTTP        bool `json:"http,omitempty"`
	Persist     bool `json:"persist,omitempty"`
}

type MapCreateResult struct {
	MutationResult
	Proxy state.Proxy `json:"proxy"`
}

type MapStopParams struct {
	ID         *string `json:"id,omitempty"`
	ListenPort *int    `json:"listen_port,omitempty"`
	All        bool    `json:"all,omitempty"`
}

type MapStopResult struct {
	MutationResult
	Stopped []string `json:"stopped"`
}

type MapListResult struct {
	Proxies []state.Proxy `json:"proxies"`
}

type MapRequestsParams struct {
	ID string `json:"id"`
}

type MapRequestsResult struct {
	SubscriptionID string `json:"subscription_id"`
}

type MapRequestsChunk struct {
	TS         string `json:"ts"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	Bytes      int64  `json:"bytes"`
}

type MapRequestsEnd struct {
	Requests int64 `json:"requests"`
}
