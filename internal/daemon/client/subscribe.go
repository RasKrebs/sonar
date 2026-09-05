package client

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// SubscribeOptions mirrors state.subscribe's params.
type SubscribeOptions struct {
	// Stats and Health map onto the wire's include: ["stats", "health"].
	Stats  bool
	Health bool
	// Events asks for state.event notifications alongside the deltas.
	Events bool
	// Buffer sizes the delta and event channels. Zero uses 64.
	Buffer int
}

func (o SubscribeOptions) include() rpc.Include {
	inc := rpc.Include{}
	if o.Stats {
		inc = append(inc, "stats")
	}
	if o.Health {
		inc = append(inc, "health")
	}
	return inc
}

// Subscription is a live view of daemon state: the snapshot that opened it plus
// the two notification channels. Both channels close when the subscription is
// cancelled or the connection drops.
type Subscription struct {
	// Snapshot is the full state at the moment the subscription was made. The
	// first delta on Deltas continues from its Seq.
	Snapshot state.Snapshot
	// Deltas carries state.delta notifications.
	Deltas <-chan state.Delta
	// Events carries state.event notifications; nil unless Events was asked
	// for.
	Events <-chan state.Event

	c       *Client
	deltas  chan state.Delta
	events  chan state.Event
	once    sync.Once
	dropped bool
	mu      sync.Mutex
}

// Subscribe registers for state deltas. The returned Subscription carries the
// opening snapshot and the delta and event channels.
func (c *Client) Subscribe(ctx context.Context, opts SubscribeOptions) (*Subscription, error) {
	buffer := opts.Buffer
	if buffer <= 0 {
		buffer = 64
	}
	s := &Subscription{
		c:      c,
		deltas: make(chan state.Delta, buffer),
	}
	s.Deltas = s.deltas
	if opts.Events {
		s.events = make(chan state.Event, buffer)
		s.Events = s.events
	}

	// Register before calling, so a delta that arrives between the daemon
	// queuing our snapshot and our reading the reply is not dropped.
	c.mu.Lock()
	c.subs = append(c.subs, s)
	c.mu.Unlock()

	var snap state.Snapshot
	err := c.Call(ctx, "state.subscribe", rpc.StateSubscribeParams{
		Include: opts.include(),
		Events:  opts.Events,
	}, &snap)
	if err != nil {
		s.remove()
		return nil, err
	}
	s.Snapshot = snap
	return s, nil
}

// Unsubscribe stops the subscription and closes its channels.
func (s *Subscription) Unsubscribe(ctx context.Context) error {
	err := s.c.Call(ctx, "state.unsubscribe", rpc.Empty{}, nil)
	s.remove()
	s.close()
	return err
}

// Dropped reports whether the subscription lost notifications because the
// consumer stopped reading its channels.
func (s *Subscription) Dropped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *Subscription) remove() {
	s.c.mu.Lock()
	kept := s.c.subs[:0]
	for _, other := range s.c.subs {
		if other != s {
			kept = append(kept, other)
		}
	}
	s.c.subs = kept
	s.c.mu.Unlock()
}

func (s *Subscription) close() {
	s.once.Do(func() {
		close(s.deltas)
		if s.events != nil {
			close(s.events)
		}
	})
}

// deliver routes one notification onto the right channel. A full channel drops
// the message and sets Dropped rather than blocking the read loop, which would
// stall every other call on this connection.
func (s *Subscription) deliver(msg rpc.Message) {
	switch msg.Method {
	case rpc.MethodStateDelta:
		var d state.Delta
		if err := json.Unmarshal(msg.Params, &d); err != nil {
			return
		}
		select {
		case s.deltas <- d:
		default:
			s.markDropped()
		}
	case rpc.MethodStateEvent:
		if s.events == nil {
			return
		}
		var e state.Event
		if err := json.Unmarshal(msg.Params, &e); err != nil {
			return
		}
		select {
		case s.events <- e:
		default:
			s.markDropped()
		}
	}
}

func (s *Subscription) markDropped() {
	s.mu.Lock()
	s.dropped = true
	s.mu.Unlock()
}
