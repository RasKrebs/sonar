package daemon

import (
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/store"
)

// Runtime is what the daemon hands to extension packages (contract §8). Step
// 1A.1 exposed the scanner, the logger and the process identity and 1A.4 the
// store; the process table, the port allocator and the spawn function are added
// by the steps that introduce them.
type Runtime struct {
	Version    string
	Socket     string
	BinaryPath string
	PID        int
	StartedAt  time.Time
	Logger     *slog.Logger
	Scanner    *scanner.Loop

	// Store is the daemon's SQLite database: renames, group pins, the port
	// history ring and the known `.sonar.yaml` roots. Nil when the database
	// could not be opened; every user of it must cope with that.
	Store *store.Store
	// DB is the raw handle contract §8 promises extension packages. It is the
	// same connection pool Store uses.
	DB  *sql.DB
	srv *Server

	runsMu sync.RWMutex
	runs   groups.Registry
}

// SetRuns installs the registry that attributes ports to `sonar start` runs.
// Step 1A.5 calls it from its OnStart hook; until then the scanner falls back
// to the attribution the scan itself recorded on the port.
func (r *Runtime) SetRuns(reg groups.Registry) {
	r.runsMu.Lock()
	defer r.runsMu.Unlock()
	r.runs = reg
}

// RunRegistry is the installed run registry, or nil. The scanner reads it once
// per tick, so a registry installed after the daemon is up takes effect on the
// next scan.
func (r *Runtime) RunRegistry() groups.Registry {
	r.runsMu.RLock()
	defer r.runsMu.RUnlock()
	return r.runs
}

// DBPath is the database file backing this daemon, or "" when there is none.
// daemon.status reports it.
func (r *Runtime) DBPath() string {
	if r.Store == nil {
		return ""
	}
	return r.Store.DBPath()
}

// Server returns the running server. Handlers use it for subscriber counts and
// for shutdown.
func (r *Runtime) Server() *Server { return r.srv }

// Subscribers is the number of connections currently subscribed to state.
func (r *Runtime) Subscribers() int { return r.srv.Subscribers() }

// Uptime is how long the daemon has been running.
func (r *Runtime) Uptime() time.Duration { return time.Since(r.StartedAt) }

var (
	hooksMu       sync.Mutex
	startHooks    []func(*Runtime)
	shutdownHooks []func(graceful bool)
)

// OnStart registers a callback run once the socket is listening and the
// runtime is complete, before the first connection is accepted (contract §8).
func OnStart(f func(*Runtime)) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	startHooks = append(startHooks, f)
}

// OnShutdown registers a callback run as the daemon stops. graceful is false
// when the daemon is unwinding after a failure.
func OnShutdown(f func(graceful bool)) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	shutdownHooks = append(shutdownHooks, f)
}

func runStartHooks(rt *Runtime) {
	hooksMu.Lock()
	hooks := append([]func(*Runtime){}, startHooks...)
	hooksMu.Unlock()
	for _, h := range hooks {
		h(rt)
	}
}

// runShutdownHooks runs the shutdown callbacks in reverse registration order,
// so a package that set something up in OnStart tears it down after the
// packages that depend on it.
func runShutdownHooks(graceful bool) {
	hooksMu.Lock()
	hooks := append([]func(bool){}, shutdownHooks...)
	hooksMu.Unlock()
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i](graceful)
	}
}
