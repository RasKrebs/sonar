package remote

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
)

// The manager as the daemon's remote router (daemon.Router). Everything the
// dispatcher needs in order to send a method somewhere else is here: whether a
// name is a host, and how to run one call on it — including a streaming one,
// where the chunks have to keep arriving after the reply.

// Known reports whether name is a registered host. The routing layer asks
// before reading a `"<host>/…"` prefix off a key, so a group whose name happens
// to look like one is never mistaken for a remote row.
func (m *Manager) Known(name string) bool { return m.Has(name) }

// Forward runs one method on a host's daemon. A streaming method answers with
// a subscription id and keeps going, and the returned stream is how the local
// daemon relays it; everything else answers once, with a nil stream.
func (m *Manager) Forward(ctx context.Context, host, method string, params json.RawMessage) (json.RawMessage, daemon.RemoteStream, error) {
	m.mu.RLock()
	b, ok := m.bridges[host]
	m.mu.RUnlock()
	if !ok {
		return nil, nil, ErrUnknownHost
	}
	return b.Forward(ctx, method, params)
}

// Forward is Call, with the streaming half. It fails fast while the bridge is
// down for the same reason Call does: a caller wants to hear that the host is
// unreachable, not to wait for it.
func (b *bridge) Forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, daemon.RemoteStream, error) {
	cli, err := b.client()
	if err != nil {
		return nil, nil, err
	}
	raw, st, err := cli.CallStream(ctx, method, params)
	if err != nil {
		return nil, nil, err
	}
	if st == nil {
		return raw, nil, nil
	}
	return raw, newRelay(st), nil
}

// relay adapts a client stream to the interface the daemon's routing layer
// consumes, which cannot name a client type: internal/daemon/client imports
// internal/daemon, so the dependency only points one way.
type relay struct {
	stream *client.Stream
	ends   chan daemon.RemoteStreamEnd
	once   sync.Once
}

func newRelay(s *client.Stream) *relay {
	r := &relay{stream: s, ends: make(chan daemon.RemoteStreamEnd, 1)}
	go func() {
		defer close(r.ends)
		for end := range s.End() {
			r.ends <- daemon.RemoteStreamEnd{Data: end.Data, Err: end.Err}
		}
		// The far side is finished, so the client-side pump can go too.
		s.Close()
	}()
	return r
}

func (r *relay) Chunks() <-chan json.RawMessage { return r.stream.Chunks() }

func (r *relay) End() <-chan daemon.RemoteStreamEnd { return r.ends }

// Cancel asks the remote daemon to stop the stream. It is sent once however
// many times the relay is torn down.
func (r *relay) Cancel(ctx context.Context) error {
	var err error
	r.once.Do(func() { err = r.stream.Cancel(ctx) })
	return err
}
