package mcpserver_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/mcpserver/fakedaemon"
	"github.com/raskrebs/sonar/internal/state"
)

// watcher is the harness of the subscription tests: the same real server over
// a real transport, with a client that records every resources/updated it is
// sent.
type watcher struct {
	*harness
	updates chan string
}

func newWatcher(t *testing.T) *watcher { return newWatcherWith(t, fakedaemon.DefaultFixture()) }

func newWatcherWith(t *testing.T, fx fakedaemon.Fixture) *watcher {
	t.Helper()

	fake := fakedaemon.New(fx)
	if err := fake.Start(); err != nil {
		t.Fatalf("starting the fake daemon: %v", err)
	}
	t.Cleanup(fake.Close)

	logs := &bytes.Buffer{}
	ctx := context.Background()
	server, err := mcpserver.New(ctx, mcpserver.Options{
		Version: "test",
		Logger:  mcpserver.NewLogger(logs, slog.LevelDebug, true),
		DaemonOptions: mcpserver.DaemonOptions{
			Socket:      fake.Addr(),
			NoAutostart: true,
			Timeout:     5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("starting the MCP server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.MCP().Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("connecting the server transport: %v", err)
	}

	updates := make(chan string, 64)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"},
		&mcp.ClientOptions{
			ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
				updates <- req.Params.URI
			},
		})
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return &watcher{
		harness: &harness{t: t, fake: fake, server: server, client: session, logs: logs},
		updates: updates,
	}
}

// subscribe subscribes and waits until the server has registered it. Under the
// 2026-07-28 protocol a subscription is a long-lived request the client sends
// without awaiting a reply, so the call returning does not mean the server has
// seen it yet.
func (w *watcher) subscribe(uri string) {
	w.t.Helper()
	if err := w.client.Subscribe(context.Background(), &mcp.SubscribeParams{URI: uri}); err != nil {
		w.t.Fatalf("subscribing to %s: %v", uri, err)
	}
	waitFor(w.t, 5*time.Second, "the server to register the subscription to "+uri, func() bool {
		return slices.Contains(w.server.SubscribedURIs(), uri)
	})
	waitFor(w.t, 5*time.Second, "the daemon to see a subscriber", func() bool {
		return w.fake.Subscribers() == 1
	})
}

func (w *watcher) unsubscribe(uri string) {
	w.t.Helper()
	if err := w.client.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri}); err != nil {
		w.t.Fatalf("unsubscribing from %s: %v", uri, err)
	}
	waitFor(w.t, 5*time.Second, "the server to drop the subscription to "+uri, func() bool {
		return !slices.Contains(w.server.SubscribedURIs(), uri)
	})
}

// collect drains the notifications that arrive within window.
func (w *watcher) collect(window time.Duration) []string {
	var got []string
	deadline := time.After(window)
	for {
		select {
		case uri := <-w.updates:
			got = append(got, uri)
		case <-deadline:
			sort.Strings(got)
			return got
		}
	}
}

// await waits for the next notification.
func (w *watcher) await(limit time.Duration) (string, bool) {
	select {
	case uri := <-w.updates:
		return uri, true
	case <-time.After(limit):
		return "", false
	}
}

// TestSubscribeOpensTheDaemonSubscriptionAndUnsubscribeClosesIt is the gating
// rule of contract §30: `sonar mcp` holds a state.subscribe only while a
// client is watching something, because a subscriber is activity against the
// daemon's idle timeout.
func TestSubscribeOpensTheDaemonSubscriptionAndUnsubscribeClosesIt(t *testing.T) {
	w := newWatcher(t)

	if w.fake.Subscribers() != 0 {
		t.Fatal("the server subscribed to the daemon before any client asked for a resource")
	}

	w.subscribe(mcpserver.URIPorts)
	w.subscribe(mcpserver.URIGroups)
	if calls := w.fake.Calls("state.subscribe"); calls != 1 {
		t.Errorf("state.subscribe was called %d times for two resources, want 1", calls)
	}

	w.unsubscribe(mcpserver.URIPorts)
	// One resource is still watched: the daemon subscription stays.
	time.Sleep(200 * time.Millisecond)
	if w.fake.Subscribers() != 1 {
		t.Fatal("the daemon subscription was dropped while a resource was still watched")
	}

	w.unsubscribe(mcpserver.URIGroups)
	waitFor(t, 5*time.Second, "the daemon subscription to be closed", func() bool {
		return w.fake.Subscribers() == 0
	})
	if calls := w.fake.Calls("state.unsubscribe"); calls != 1 {
		t.Errorf("state.unsubscribe was called %d times, want 1", calls)
	}
}

// TestSubscribingToAnUnknownURIIsRefused: the SDK does not check the URI of a
// subscription, and a subscription to a URI that will never be announced is a
// silent hang, so the server checks.
//
// The refusal is not visible to the client under the 2026-07-28 protocol,
// where a subscription is a long-lived request whose failure the SDK does not
// report back; what is visible is that nothing was subscribed to — no daemon
// subscription was opened, and no notification is ever sent for the URI.
func TestSubscribingToAnUnknownURIIsRefused(t *testing.T) {
	w := newWatcher(t)

	_ = w.client.Subscribe(context.Background(), &mcp.SubscribeParams{URI: "sonar://nonsense"})
	time.Sleep(200 * time.Millisecond)

	if calls := w.fake.Calls("state.subscribe"); calls != 0 {
		t.Errorf("a refused subscription opened %d daemon subscriptions, want 0", calls)
	}
	w.fake.Push(state.Delta{Ports: state.Change[state.Port]{Added: fakedaemon.ManyPorts(1)}})
	if got := w.collect(400 * time.Millisecond); len(got) != 0 {
		t.Errorf("a refused subscription still received %v", got)
	}
}

// TestDeltaNotifiesExactlyTheAffectedURIs is the step's demo in miniature: a
// delta from the daemon produces resources/updated in the client, for the URIs
// the delta touched and no others.
func TestDeltaNotifiesExactlyTheAffectedURIs(t *testing.T) {
	w := newWatcherWith(t, withSessions(fakedaemon.DefaultFixture()))
	for _, uri := range []string{
		mcpserver.URIPorts,
		mcpserver.URIGroups,
		mcpserver.URISessions,
		mcpserver.GroupURI("shop"),
		mcpserver.GroupURI("shop-infra"),
	} {
		w.subscribe(uri)
	}

	// A port appearing inside an existing group: the ports collection and that
	// one group changed. The group list did not: no group came or went.
	w.fake.Push(state.Delta{
		Ports:  state.Change[state.Port]{Added: fakedaemon.ManyPorts(1)},
		Groups: state.Change[state.Group]{Updated: []state.Group{fakedaemon.DefaultGroups()[0]}},
	})

	want := []string{mcpserver.GroupURI("shop"), mcpserver.URIPorts}
	sort.Strings(want)
	if got := w.collect(600 * time.Millisecond); !slices.Equal(got, want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}

	// A group appearing, and a session with it: now the collections change too.
	w.fake.Push(state.Delta{
		Groups:   state.Change[state.Group]{Added: []state.Group{{Name: "new-thing"}}},
		Sessions: state.Change[state.SessionRecord]{Updated: fakedaemon.DefaultSessions()},
	})

	// sonar://groups/new-thing is nobody's subscription, so it is not sent.
	want = []string{mcpserver.URIGroups, mcpserver.URISessions}
	sort.Strings(want)
	if got := w.collect(600 * time.Millisecond); !slices.Equal(got, want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}
}

// TestNotificationsAreCoalesced pins the 1/s rule of spec 2 section 1.2: a
// burst of deltas is one notification, and the changes that arrived during the
// window are announced once at the end of it rather than dropped.
func TestNotificationsAreCoalesced(t *testing.T) {
	w := newWatcher(t)
	w.subscribe(mcpserver.URIPorts)

	start := time.Now()
	for range 3 {
		w.fake.Push(state.Delta{Ports: state.Change[state.Port]{Added: fakedaemon.ManyPorts(1)}})
		time.Sleep(100 * time.Millisecond)
	}

	if got := w.collect(300 * time.Millisecond); len(got) != 1 {
		t.Fatalf("three deltas in 300 ms produced %d notifications (%v), want 1", len(got), got)
	}

	// The two that were held back are one trailing notification, no earlier
	// than a second after the first went out.
	uri, ok := w.await(2 * time.Second)
	if !ok {
		t.Fatal("the coalesced changes were never announced")
	}
	if uri != mcpserver.URIPorts {
		t.Fatalf("trailing update = %q, want %q", uri, mcpserver.URIPorts)
	}
	if elapsed := time.Since(start); elapsed < mcpserver.NotifyInterval {
		t.Errorf("the trailing update came after %s, want at least %s", elapsed, mcpserver.NotifyInterval)
	}
	if extra := w.collect(300 * time.Millisecond); len(extra) != 0 {
		t.Errorf("a quiet daemon still sent %v", extra)
	}
}

// TestReconnectReplaysEverySubscribedURI: the deltas from the gap are gone, so
// after a reconnect every subscribed URI is announced exactly once and the
// client re-reads (spec 2, "Runtime").
func TestReconnectReplaysEverySubscribedURI(t *testing.T) {
	w := newWatcher(t)
	w.subscribe(mcpserver.URIPorts)
	w.subscribe(mcpserver.GroupURI("shop"))

	// Subscribing is not itself news: nothing changed yet.
	if got := w.collect(400 * time.Millisecond); len(got) != 0 {
		t.Fatalf("subscribing produced %v, want nothing until something changes", got)
	}

	w.fake.Stop()
	if err := w.fake.Restart(); err != nil {
		t.Fatalf("restarting the fake daemon: %v", err)
	}
	waitFor(t, 10*time.Second, "the server to reconnect", w.server.Daemon().Connected)

	want := []string{mcpserver.GroupURI("shop"), mcpserver.URIPorts}
	sort.Strings(want)
	got := w.collect(2 * time.Second)
	if !slices.Equal(got, want) {
		t.Fatalf("after a reconnect updates = %v, want exactly %v", got, want)
	}

	// And the subscription is live again on the new connection.
	w.fake.Push(state.Delta{Ports: state.Change[state.Port]{Added: fakedaemon.ManyPorts(1)}})
	if uri, ok := w.await(3 * time.Second); !ok || uri != mcpserver.URIPorts {
		t.Fatalf("after a reconnect a delta produced %q (%v), want %s", uri, ok, mcpserver.URIPorts)
	}
}

// TestClosingTheClientReleasesTheDaemon: a client that exits without
// unsubscribing must not leave the daemon holding a subscriber for it.
func TestClosingTheClientReleasesTheDaemon(t *testing.T) {
	w := newWatcher(t)
	w.subscribe(mcpserver.URIPorts)

	if err := w.client.Close(); err != nil {
		t.Fatalf("closing the client session: %v", err)
	}
	waitFor(t, 5*time.Second, "the daemon subscription to be released", func() bool {
		return w.fake.Subscribers() == 0
	})
}
