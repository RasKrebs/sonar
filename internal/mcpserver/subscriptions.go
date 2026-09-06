package mcpserver

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/state"
)

// NotifyInterval is the coalescing window of spec 2, section 1.2: at most one
// resources/updated per URI per second. A scan that finds a dozen ports
// arriving at once is one notification, not a dozen, and a client that
// re-reads on every notification is not made to re-read in a loop.
const NotifyInterval = time.Second

// subscriptions is the bridge between the two subscription models: MCP's, in
// which a client subscribes per resource URI, and the daemon's, in which one
// connection subscribes to the whole state.
//
// The bridge is deliberately lazy. The MCP server holds no state.subscribe
// until a client asks for one, and drops it again when the last subscription
// goes, because a subscriber counts as activity against the daemon's idle
// timeout (contract §15, §30): an agent that started `sonar mcp` and never
// used a resource must not keep a daemon alive for the rest of the day.
type subscriptions struct {
	srv      *Server
	interval time.Duration

	mu sync.Mutex
	// sessions maps a resource URI to the client sessions watching it. A URI
	// with no sessions is deleted, so the map is also "what is being watched".
	sessions map[string]map[*mcp.ServerSession]bool
	// watching records the sessions we have a disconnect watcher for. The SDK
	// forgets a disconnected session's subscriptions but does not call the
	// unsubscribe handler, so the count has to be corrected here or the
	// daemon subscription outlives the client that asked for it.
	watching map[*mcp.ServerSession]bool
	// sub is the daemon subscription, held while at least one URI is watched.
	sub     *StateSub
	opening bool
	// suppressResume swallows the resumed hook that fires inside the very
	// first Subscribe: that snapshot is not a reconnect, and the client that
	// just subscribed is about to read the resource anyway.
	suppressResume bool
	// lastSent and pending are the coalescer: when a URI last went out, and
	// the timer that will send the update it is currently holding back.
	lastSent map[string]time.Time
	pending  map[string]*time.Timer
}

func newSubscriptions(s *Server) *subscriptions {
	return &subscriptions{
		srv:      s,
		interval: NotifyInterval,
		sessions: map[string]map[*mcp.ServerSession]bool{},
		watching: map[*mcp.ServerSession]bool{},
		lastSent: map[string]time.Time{},
		pending:  map[string]*time.Timer{},
	}
}

// subscribe is the server's SubscribeHandler. The first subscription opens the
// daemon's state.subscribe with events off and no include: resources are the
// port, group and session collections, and asking for stats or health would
// make the daemon collect them for a client that never reads them.
func (s *subscriptions) subscribe(ctx context.Context, req *mcp.SubscribeRequest) error {
	uri := req.Params.URI
	if !knownResourceURI(uri) {
		return mcp.ResourceNotFoundError(uri)
	}

	s.mu.Lock()
	if s.sessions[uri] == nil {
		s.sessions[uri] = map[*mcp.ServerSession]bool{}
	}
	s.sessions[uri][req.Session] = true
	watch := !s.watching[req.Session]
	s.watching[req.Session] = true
	open := s.sub == nil && !s.opening
	if open {
		s.opening, s.suppressResume = true, true
	}
	s.mu.Unlock()

	if watch {
		go s.watchSession(req.Session)
	}
	if open {
		s.open(ctx)
	}
	return nil
}

// open issues the daemon subscription. A daemon that is away right now is not
// a reason to refuse the client's subscription: the handle is registered
// either way, the reconnect loop re-issues it, and the resumed hook then tells
// every subscriber to re-read (spec 2, "Runtime").
func (s *subscriptions) open(ctx context.Context) {
	sub, err := s.srv.daemon.Subscribe(ctx, client.SubscribeOptions{}, s.delta, s.resumed)

	s.mu.Lock()
	s.opening, s.suppressResume = false, false
	s.sub = sub
	s.mu.Unlock()

	if err != nil {
		s.srv.log.Warn("the daemon subscription behind the resources could not be opened",
			"error", err)
		return
	}
	s.srv.log.Debug("opened the daemon state subscription for resource subscribers")
}

// unsubscribe is the server's UnsubscribeHandler.
func (s *subscriptions) unsubscribe(_ context.Context, req *mcp.UnsubscribeRequest) error {
	s.drop(req.Session, req.Params.URI)
	return nil
}

// drop removes one session's interest in uris and closes the daemon
// subscription when nothing is left watching.
//
// It deliberately makes its own context rather than borrowing the caller's:
// an unsubscribe arrives as the cancellation of the client's subscription
// stream, so the context that carries it is already dead, and telling the
// daemon to stop is exactly the call that must still go out.
func (s *subscriptions) drop(session *mcp.ServerSession, uris ...string) {
	s.mu.Lock()
	for _, uri := range uris {
		watchers := s.sessions[uri]
		if watchers == nil {
			continue
		}
		delete(watchers, session)
		if len(watchers) > 0 {
			continue
		}
		delete(s.sessions, uri)
		if timer := s.pending[uri]; timer != nil {
			timer.Stop()
			delete(s.pending, uri)
		}
		delete(s.lastSent, uri)
	}
	idle := len(s.sessions) == 0
	sub := s.sub
	if idle {
		s.sub = nil
	}
	s.mu.Unlock()

	if !idle || sub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	if err := sub.Close(ctx); err != nil {
		s.srv.log.Warn("closing the daemon state subscription failed", "error", err)
		return
	}
	s.srv.log.Debug("closed the daemon state subscription: nothing is subscribed")
}

// watchSession drops a client's subscriptions when its session ends. Without
// it a client that exits without unsubscribing would leave the daemon
// subscription open for the life of the process.
func (s *subscriptions) watchSession(session *mcp.ServerSession) {
	_ = session.Wait()

	s.mu.Lock()
	delete(s.watching, session)
	var uris []string
	for uri, watchers := range s.sessions {
		if watchers[session] {
			uris = append(uris, uri)
		}
	}
	s.mu.Unlock()
	if len(uris) == 0 {
		return
	}
	s.drop(session, uris...)
}

// uris is the set of resource URIs clients are watching right now, sorted.
func (s *subscriptions) uris() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sessions))
	for uri := range s.sessions {
		out = append(out, uri)
	}
	slices.Sort(out)
	return out
}

// delta is the state.delta hook: one notification per subscribed URI the
// delta touched.
func (s *subscriptions) delta(d state.Delta) {
	for _, uri := range s.affected(d) {
		s.notify(uri)
	}
}

// resumed is the reconnect hook. Deltas from the gap are gone, so everything
// a client holds is suspect: every subscribed URI is announced once, ahead of
// the coalescer rather than through it, so a reconnect always produces exactly
// one notification per URI (spec 2, "Runtime").
func (s *subscriptions) resumed(state.Snapshot) {
	s.mu.Lock()
	if s.suppressResume {
		s.suppressResume = false
		s.mu.Unlock()
		return
	}
	uris := make([]string, 0, len(s.sessions))
	now := time.Now()
	for uri := range s.sessions {
		uris = append(uris, uri)
		s.lastSent[uri] = now
	}
	for uri, timer := range s.pending {
		timer.Stop()
		delete(s.pending, uri)
	}
	s.mu.Unlock()

	slices.Sort(uris) // a stable order, so a transcript reads the same twice
	for _, uri := range uris {
		s.send(uri)
	}
}

// affected maps one delta onto the resource URIs it changed, keeping only URIs
// somebody is actually watching.
//
// `sonar://groups` follows spec 2 literally: it is announced when a group
// appears or disappears, not when one is updated in place, because a group
// whose member ports changed is news for that group's own URI and for
// `sonar://ports`, both of which are announced.
func (s *subscriptions) affected(d state.Delta) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []string
	add := func(uri string) {
		if len(s.sessions[uri]) == 0 || slices.Contains(out, uri) {
			return
		}
		out = append(out, uri)
	}

	if changed(d.Ports) {
		add(URIPorts)
	}
	for _, g := range d.Groups.Added {
		add(GroupURI(g.Name))
	}
	for _, g := range d.Groups.Updated {
		add(GroupURI(g.Name))
	}
	for _, name := range d.Groups.Removed {
		add(GroupURI(name))
	}
	if len(d.Groups.Added) > 0 || len(d.Groups.Removed) > 0 {
		add(URIGroups)
	}
	if changed(d.Sessions) {
		add(URISessions)
	}
	return out
}

func changed[T any](c state.Change[T]) bool {
	return len(c.Added) > 0 || len(c.Updated) > 0 || len(c.Removed) > 0
}

// notify sends one update for uri, or holds it back to the end of the current
// second when one already went out. The first change in a quiet period is
// delivered immediately — an agent waiting for a server to come up should not
// wait a second to hear about it — and everything after it is folded into one
// trailing notification.
func (s *subscriptions) notify(uri string) {
	s.mu.Lock()
	last, seen := s.lastSent[uri]
	now := time.Now()
	switch {
	case !seen || now.Sub(last) >= s.interval:
		s.lastSent[uri] = now
		s.mu.Unlock()
		s.send(uri)
		return
	case s.pending[uri] != nil:
		// A trailing notification is already scheduled: this change joins it.
		s.mu.Unlock()
		return
	}
	s.pending[uri] = time.AfterFunc(s.interval-now.Sub(last), func() { s.flush(uri) })
	s.mu.Unlock()
}

func (s *subscriptions) flush(uri string) {
	s.mu.Lock()
	delete(s.pending, uri)
	watched := len(s.sessions[uri]) > 0
	if watched {
		s.lastSent[uri] = time.Now()
	}
	s.mu.Unlock()
	if watched {
		s.send(uri)
	}
}

func (s *subscriptions) send(uri string) {
	err := s.srv.mcp.ResourceUpdated(context.Background(),
		&mcp.ResourceUpdatedNotificationParams{URI: uri})
	if err != nil {
		s.srv.log.Warn("sending a resource update failed", "uri", uri, "error", err)
	}
}
