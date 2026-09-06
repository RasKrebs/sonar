// Package mcpserver implements `sonar mcp`: a stdio MCP server, compiled into
// the sonar binary, that is a thin client of the daemon (spec 2, section 1).
//
// It scans nothing and kills nothing itself. Every tool is one daemon call
// whose structured result is the daemon's own JSON, so an agent, the CLI's
// `--json` and the desktop app all see one vocabulary. What this package adds
// is the parts an agent needs and a wire protocol does not have: tool
// descriptions written for a model, a text rendering of every result, and an
// error model that tells "the daemon said no" apart from "the call itself
// failed".
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/invopop/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the implementation name clients see, and the key
// `sonar install mcp` registers in every client config.
const ServerName = "sonar"

// Instructions is the server-level guidance sent at initialize. It is the same
// advice as the bundled skill, compressed: what sonar is for, and the mistake
// an agent makes without it.
const Instructions = `sonar is the local registry of everything listening on this machine's ports: ` +
	`dev servers, containers, system services, and whatever an agent started earlier.

Use list_ports instead of lsof, netstat or ps: the rows carry the project, the group, ` +
	`the run and the agent session behind each port, which a process listing does not.
Use inspect_port before acting on a port. "Address already in use" is a question about ` +
	`who owns the port, not a reason to kill it: a port owned by another session, another ` +
	`worktree or a human is not yours to free.

Results are structured: the JSON in structuredContent is the same shape as ` + "`sonar list --json`" + `, ` +
	`and the text block is a summary for reading. Errors come back as a result with an ` +
	`error code (not_found, ambiguous, invalid_arguments, daemon_unavailable) and a hint; ` +
	`daemon_unavailable is transient and worth one retry.`

// Options configures the server.
type Options struct {
	// Version is reported as the implementation version and to the daemon.
	Version string
	// Logger receives everything the server says. It must write to stderr:
	// stdout is the MCP transport. Nil discards.
	Logger *slog.Logger
	// Daemon is the daemon connection to use. Nil dials one from DaemonOptions.
	Daemon *Daemon
	// DaemonOptions configures the connection when Daemon is nil.
	DaemonOptions DaemonOptions
}

// Server is the MCP server plus the daemon connection behind it.
type Server struct {
	mcp    *mcp.Server
	daemon *Daemon
	log    *slog.Logger
	// ownsDaemon records whether Close should disconnect: a caller who passed
	// their own connection keeps it.
	ownsDaemon bool
}

// New connects to the daemon and builds the MCP server with its tools
// registered. The daemon connection is made eagerly, and its failure is fatal,
// because a server that cannot reach the daemon has nothing to answer with.
func New(ctx context.Context, opts Options) (*Server, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	d, owns := opts.Daemon, false
	if d == nil {
		dopts := opts.DaemonOptions
		dopts.Version = opts.Version
		dopts.Logger = log
		var err error
		if d, err = ConnectDaemon(ctx, dopts); err != nil {
			return nil, err
		}
		owns = true
	}

	s := &Server{
		mcp: mcp.NewServer(
			&mcp.Implementation{
				Name:    ServerName,
				Title:   "sonar",
				Version: opts.Version,
			},
			&mcp.ServerOptions{Instructions: Instructions, Logger: log},
		),
		daemon:     d,
		log:        log,
		ownsDaemon: owns,
	}
	s.addReadTools()
	return s, nil
}

// MCP exposes the underlying server so later slices can add resources and
// prompts, and so tests can connect an in-memory transport.
func (s *Server) MCP() *mcp.Server { return s.mcp }

// Daemon exposes the daemon connection.
func (s *Server) Daemon() *Daemon { return s.daemon }

// Run serves the transport until the client disconnects or ctx is cancelled.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	defer s.Close()
	return s.mcp.Run(ctx, t)
}

// Close disconnects from the daemon if this server opened the connection.
func (s *Server) Close() error {
	if s.ownsDaemon {
		return s.daemon.Close()
	}
	return nil
}

// schemaReflector produces the output schemas of the tools. The state and rpc
// types carry invopop `jsonschema` tags (nullability, enums) that the SDK's own
// reflector reads as descriptions and rejects, so the schemas are built with
// the same reflector `go generate` uses for docs/schema/protocol.schema.json.
// One reflector, one published shape.
var schemaReflector = &jsonschema.Reflector{
	// Additive protocol changes must not fail validation (contract §7), and
	// `sonar list --json` carries deprecated flat fields beside the contract
	// ones.
	AllowAdditionalProperties: true,
	ExpandedStruct:            true,
}

// outputSchema reflects v into a JSON Schema for a tool's OutputSchema. It
// panics on a type it cannot reflect, which is a programming error caught by
// the first test that builds a server.
func outputSchema(v any) json.RawMessage {
	raw, err := json.Marshal(schemaReflector.Reflect(v))
	if err != nil {
		panic(fmt.Sprintf("mcpserver: reflecting %T: %v", v, err))
	}
	return raw
}

// boolPtr is for the annotation fields the SDK models as *bool, where nil,
// false and true are three different statements.
func boolPtr(b bool) *bool { return &b }
