package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/state"
)

// DefaultTimeout bounds one daemon call (spec 1, "Concurrency and timeouts").
// Tools that wait on purpose carry their own timeout argument instead.
const DefaultTimeout = 10 * time.Second

// backoffSchedule is the reconnect delay sequence from spec 1: 250 ms, 500 ms,
// 1 s, then every 2 s for as long as the daemon stays away.
var backoffSchedule = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
}

// DaemonOptions configures the one connection the MCP server keeps open.
type DaemonOptions struct {
	// Socket overrides the resolved daemon address.
	Socket string
	// NoAutostart makes the first connect fail instead of spawning
	// `sonar serve --detach`; reconnects then wait for someone else to start
	// the daemon.
	NoAutostart bool
	// Timeout bounds one call. Zero uses DefaultTimeout.
	Timeout time.Duration
	// Version is reported to the daemon in daemon.hello.
	Version string
	// Logger receives connection lifecycle messages. Never nil after Connect.
	Logger *slog.Logger
}

// Daemon is the MCP server's client of the sonar daemon: one connection,
// multiplexed by the client package, plus the reconnect loop spec 1 asks for.
//
// The distinction that matters is between *not having* a connection and a
// daemon that answered with an error. The first is a domain result
// (`daemon_unavailable`) an agent can retry; the second is the daemon's own
// contract §2 error passed through. Neither is ever a protocol failure once
// the server has started.
type Daemon struct {
	opts DaemonOptions
	log  *slog.Logger

	mu      sync.Mutex
	cur     *client.Client
	subs    []*persistentSub
	closed  bool
	closeCh chan struct{}

	// reconnected counts completed reconnects, for tests and for the log.
	reconnects int
}

// persistentSub is a state.subscribe that survives reconnects: on every new
// connection it is re-issued and the caller is told, so a resource-serving
// layer can re-send ResourceUpdated for everything it holds.
type persistentSub struct {
	opts    client.SubscribeOptions
	deltas  func(state.Delta)
	resumed func(state.Snapshot)

	mu     sync.Mutex
	sub    *client.Subscription
	cancel context.CancelFunc
}

// ConnectDaemon opens the connection, autostarting `sonar serve --detach` when
// nothing is listening (contract §7). A failure here is fatal: unlike the
// CLI's read commands, which fall back to a direct scan (contract §20), the
// MCP server has no scanner of its own and nothing to serve without a daemon.
func ConnectDaemon(ctx context.Context, opts DaemonOptions) (*Daemon, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	d := &Daemon{opts: opts, log: opts.Logger, closeCh: make(chan struct{})}

	c, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	d.cur = c
	d.log.Info("connected to the daemon",
		"socket", c.Socket(), "daemon_version", c.Hello().DaemonVersion)
	go d.watch(c)
	return d, nil
}

func (d *Daemon) dial(ctx context.Context) (*client.Client, error) {
	return client.Connect(ctx, client.ClientInfo{
		Name:        "mcp",
		Version:     d.opts.Version,
		Socket:      d.opts.Socket,
		NoAutostart: d.opts.NoAutostart,
	})
}

// Call sends one request. It returns nil, a *rpc.Error the daemon produced, or
// a *DomainError when the connection is down or the per-call timeout expired.
func (d *Daemon) Call(ctx context.Context, method string, params, out any) error {
	c := d.current()
	if c == nil {
		return d.unavailable()
	}
	callCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()

	err := c.Call(callCtx, method, params, out)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		// The MCP client went away or cancelled; that is not a daemon fault.
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return Domain(CodeTimeout,
			fmt.Sprintf("the daemon did not answer %s within %s", method, d.opts.Timeout),
			"the daemon may be scanning a busy machine; retry, or check `sonar daemon status`")
	}
	if _, ok := asDomain(err); ok {
		return err // a daemon error object: pass the contract code through
	}
	// Anything else means the connection went away underneath the call.
	d.log.Warn("daemon call failed on a dropped connection", "method", method, "error", err)
	return d.unavailable()
}

// Capabilities is the daemon's capability list from daemon.hello, empty while
// disconnected.
func (d *Daemon) Capabilities() []string {
	c := d.current()
	if c == nil {
		return nil
	}
	return c.Hello().Capabilities
}

// Has reports whether the daemon advertises a capability. Tools gate on this
// rather than on the schema (contract §21).
func (d *Daemon) Has(capability string) bool {
	for _, c := range d.Capabilities() {
		if c == capability {
			return true
		}
	}
	return false
}

// Connected reports whether a call would reach the daemon right now.
func (d *Daemon) Connected() bool { return d.current() != nil }

// Reconnects counts the times the connection was re-established.
func (d *Daemon) Reconnects() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reconnects
}

// Subscribe opens a state.subscribe that is re-issued after every reconnect.
// deltas receives every state.delta; resumed, if set, is called with the fresh
// snapshot each time the subscription is re-established, which is a
// resource-serving layer's cue to re-send ResourceUpdated for every URI it has
// handed out (spec 1).
func (d *Daemon) Subscribe(ctx context.Context, opts client.SubscribeOptions,
	deltas func(state.Delta), resumed func(state.Snapshot)) error {

	ps := &persistentSub{opts: opts, deltas: deltas, resumed: resumed}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("mcpserver: the daemon connection is closed")
	}
	c := d.cur
	d.subs = append(d.subs, ps)
	d.mu.Unlock()

	if c == nil {
		return d.unavailable()
	}
	return ps.open(ctx, c)
}

// Close disconnects and stops reconnecting.
func (d *Daemon) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	c := d.cur
	d.cur = nil
	subs := d.subs
	d.subs = nil
	d.mu.Unlock()

	close(d.closeCh)
	for _, ps := range subs {
		ps.stop()
	}
	if c != nil {
		return c.Close()
	}
	return nil
}

func (d *Daemon) current() *client.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur
}

// unavailable is the domain error a call gets while there is no connection.
// It names the log file, because the next question an agent (or the person
// reading over its shoulder) has is why the daemon went away.
func (d *Daemon) unavailable() *DomainError {
	return Domain(CodeDaemonUnavailable,
		"the sonar daemon is not reachable; reconnecting",
		"retry in a moment. If it keeps failing, run `sonar daemon status`; the daemon log is at "+daemon.LogPath())
}

// watch waits for the connection to drop and then reconnects with backoff,
// re-issuing every persistent subscription (spec 1, "Runtime"). In-flight
// calls fail while it runs, which is what makes them domain errors rather than
// protocol errors.
func (d *Daemon) watch(c *client.Client) {
	select {
	case <-c.Done():
	case <-d.closeCh:
		return
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.cur = nil
	d.mu.Unlock()
	d.log.Warn("daemon connection dropped, reconnecting", "error", c.Err())

	next, ok := d.reconnect()
	if !ok {
		return
	}
	go d.watch(next)
}

// reconnect retries until the daemon answers or the server shuts down.
func (d *Daemon) reconnect() (*client.Client, bool) {
	for attempt := 0; ; attempt++ {
		delay := backoffSchedule[min(attempt, len(backoffSchedule)-1)]
		select {
		case <-time.After(delay):
		case <-d.closeCh:
			return nil, false
		}

		ctx, cancel := context.WithTimeout(context.Background(), d.opts.Timeout)
		c, err := d.dial(ctx)
		cancel()
		if err != nil {
			d.log.Debug("reconnect failed", "attempt", attempt+1, "error", err)
			continue
		}

		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			_ = c.Close()
			return nil, false
		}
		d.cur = c
		d.reconnects++
		subs := append([]*persistentSub{}, d.subs...)
		d.mu.Unlock()

		d.log.Info("reconnected to the daemon", "socket", c.Socket(), "attempts", attempt+1)
		for _, ps := range subs {
			if err := ps.open(context.Background(), c); err != nil {
				d.log.Warn("resubscribe failed", "error", err)
			}
		}
		return c, true
	}
}

// open issues state.subscribe on c and starts pumping its deltas. It replaces
// any subscription the previous connection held.
func (ps *persistentSub) open(ctx context.Context, c *client.Client) error {
	sub, err := c.Subscribe(ctx, ps.opts)
	if err != nil {
		return err
	}

	pumpCtx, cancel := context.WithCancel(context.Background())
	ps.mu.Lock()
	if ps.cancel != nil {
		ps.cancel()
	}
	ps.sub, ps.cancel = sub, cancel
	ps.mu.Unlock()

	go ps.pump(pumpCtx, sub)
	if ps.resumed != nil {
		ps.resumed(sub.Snapshot)
	}
	return nil
}

func (ps *persistentSub) pump(ctx context.Context, sub *client.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case delta, ok := <-sub.Deltas:
			if !ok {
				return
			}
			if ps.deltas != nil {
				ps.deltas(delta)
			}
		}
	}
}

func (ps *persistentSub) stop() {
	ps.mu.Lock()
	cancel := ps.cancel
	ps.cancel = nil
	ps.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
