// Package remote is the local daemon's side of a registered SSH host: one
// bridge per host, each speaking the ordinary daemon protocol over the
// stdin/stdout of `ssh <target> sonar daemon stdio`, and a manager that
// multiplexes what those bridges report into the state stream every local
// client already reads.
//
// Nothing here listens on a network port and nothing here implements SSH. The
// system `ssh` binary is spawned so the user's `~/.ssh/config`, agent, jump
// hosts and known_hosts all apply exactly as they do from a shell — the whole
// point of the transport choice in the remote-hosts design.
//
// The package registers its own RPC handlers and its own OnStart hook from
// init(), so internal/daemon never imports it (contract §8).
package remote

import (
	"time"

	"github.com/raskrebs/sonar/internal/config"
)

// Host is one registered SSH host, as the user config spells it.
type Host = config.RemoteHost

// Reconnect timing, from the remote-hosts design: exponential backoff from one
// second to thirty, forever, per host.
const (
	ReconnectMin    = 1 * time.Second
	ReconnectMax    = 30 * time.Second
	ReconnectFactor = 2

	// HandshakeTimeout bounds `daemon.hello` over a fresh bridge. It is
	// generous because the far side may be autostarting its own daemon, and
	// because the first SSH connection to a cold host pays for the TCP
	// handshake, the key exchange and a login shell.
	HandshakeTimeout = 30 * time.Second

	// PingInterval is how often a connected bridge measures round-trip
	// latency. Latency alone never publishes a delta (state.DiffHosts ignores
	// it), so the newest value rides out with the next real change.
	PingInterval = 20 * time.Second

	// PingTimeout bounds one latency probe. A probe that does not answer in
	// this long is treated as a dead bridge.
	PingTimeout = 10 * time.Second
)
