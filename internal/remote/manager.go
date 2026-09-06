package remote

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"

	"github.com/raskrebs/sonar/internal/state"
)

// Options configures a Manager.
type Options struct {
	// Version is the local daemon's version, sent to each remote in
	// daemon.hello.
	Version string
	// Logger receives the manager's log. Nil is a discard logger.
	Logger *slog.Logger
	// Dial opens one bridge. Nil means DialSSH.
	Dial Dialer
	// OnChange is called whenever any host's rows or status moved. The daemon
	// points it at scanner.Loop.RemoteChanged, which republishes without
	// re-scanning the local machine.
	OnChange func()
	// OnEvent forwards one remote event into the local stream. The daemon
	// points it at Server.BroadcastEvent.
	OnEvent func(state.Event)
	// Save persists the host list. Nil means config.SaveRemoteHosts.
	Save func([]Host) error
}

// Manager owns one bridge per registered host and merges what they report into
// a single state.Rows the scanner folds into every published snapshot.
type Manager struct {
	opts Options
	ctx  context.Context
	stop context.CancelFunc

	mu      sync.RWMutex
	bridges map[string]*bridge
	order   []string
}

// NewManager builds a manager. It does not connect; call Start.
func NewManager(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Dial == nil {
		opts.Dial = DialSSH
	}
	if opts.OnChange == nil {
		opts.OnChange = func() {}
	}
	if opts.OnEvent == nil {
		opts.OnEvent = func(state.Event) {}
	}
	return &Manager{opts: opts, bridges: map[string]*bridge{}}
}

// Start brings up a bridge for every host in hosts and keeps them up until
// Stop. Starting twice is a no-op on the second call.
func (m *Manager) Start(ctx context.Context, hosts []Host) {
	m.mu.Lock()
	if m.stop != nil {
		m.mu.Unlock()
		return
	}
	m.ctx, m.stop = context.WithCancel(ctx)
	m.mu.Unlock()

	for _, h := range hosts {
		m.add(h)
	}
	if len(hosts) > 0 {
		m.opts.OnChange()
	}
}

// Stop tears every bridge down.
func (m *Manager) Stop() {
	m.mu.Lock()
	stop := m.stop
	m.stop = nil
	bridges := m.bridges
	m.bridges = map[string]*bridge{}
	m.order = nil
	m.mu.Unlock()

	if stop != nil {
		stop()
	}
	for _, b := range bridges {
		b.stop()
	}
}

// add registers and starts one host. The caller has checked for duplicates.
func (m *Manager) add(h Host) {
	b := newBridge(h, m.opts.Version, m.opts.Dial, m.opts.Logger, m.opts.OnChange, m.opts.OnEvent)

	m.mu.Lock()
	ctx := m.ctx
	if _, dup := m.bridges[h.Name]; dup {
		m.mu.Unlock()
		return
	}
	m.bridges[h.Name] = b
	m.order = append(m.order, h.Name)
	sort.Strings(m.order)
	m.mu.Unlock()

	if ctx != nil {
		b.start(ctx)
	}
}

// Add registers a host, persists it and connects. A name already in use is an
// error: the name is the key of everything the host contributes.
func (m *Manager) Add(h Host) error {
	m.mu.RLock()
	_, dup := m.bridges[h.Name]
	hosts := m.configsLocked()
	m.mu.RUnlock()
	if dup {
		return ErrDuplicateHost
	}

	if err := m.save(append(hosts, h)); err != nil {
		return err
	}
	m.add(h)
	m.opts.OnChange()
	return nil
}

// Remove unregisters a host: its bridge is torn down, its rows leave the
// published state on the next publish, and it is dropped from the config.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	b, ok := m.bridges[name]
	if !ok {
		m.mu.Unlock()
		return ErrUnknownHost
	}
	delete(m.bridges, name)
	kept := m.order[:0]
	for _, n := range m.order {
		if n != name {
			kept = append(kept, n)
		}
	}
	m.order = kept
	hosts := m.configsLocked()
	m.mu.Unlock()

	b.stop()
	if err := m.save(hosts); err != nil {
		return err
	}
	m.opts.OnChange()
	return nil
}

// Rows is every registered host's contribution, in host-name order so the
// published collections are stable across ticks.
func (m *Manager) Rows() state.Rows {
	m.mu.RLock()
	bridges := m.bridgesLocked()
	m.mu.RUnlock()

	var out state.Rows
	for _, b := range bridges {
		out = out.Append(b.Rows())
	}
	return out
}

// Hosts is the `remote.list` result: one Host row per registered host,
// whatever its connection state.
func (m *Manager) Hosts() []state.Host {
	m.mu.RLock()
	bridges := m.bridgesLocked()
	m.mu.RUnlock()

	out := make([]state.Host, 0, len(bridges))
	for _, b := range bridges {
		out = append(out, b.HostRow())
	}
	return out
}

// Configs is the registered host list as the config spells it.
func (m *Manager) Configs() []Host {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.configsLocked()
}

// Has reports whether name is registered.
func (m *Manager) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.bridges[name]
	return ok
}

// Call forwards one method to a host's daemon and returns its result verbatim.
func (m *Manager) Call(ctx context.Context, host, method string, params json.RawMessage) (json.RawMessage, error) {
	m.mu.RLock()
	b, ok := m.bridges[host]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownHost
	}
	return b.Call(ctx, method, params)
}

// bridgesLocked returns the bridges in host-name order. Caller holds the lock.
func (m *Manager) bridgesLocked() []*bridge {
	out := make([]*bridge, 0, len(m.order))
	for _, name := range m.order {
		if b, ok := m.bridges[name]; ok {
			out = append(out, b)
		}
	}
	return out
}

// configsLocked returns the registered host configs in name order. Caller
// holds the lock.
func (m *Manager) configsLocked() []Host {
	out := make([]Host, 0, len(m.order))
	for _, b := range m.bridgesLocked() {
		out = append(out, b.Config())
	}
	return out
}

func (m *Manager) save(hosts []Host) error {
	if m.opts.Save != nil {
		return m.opts.Save(hosts)
	}
	return saveHosts(hosts)
}
