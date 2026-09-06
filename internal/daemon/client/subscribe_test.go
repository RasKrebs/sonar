package client

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/state"
)

// TestDeliverRacesClose pins the fix for the panic PR #68 hit in CI: the read
// loop delivers a notification while the consumer unsubscribes, and the
// delivery must not send on the channels close() has just closed.
func TestDeliverRacesClose(t *testing.T) {
	delta := rpc.Message{Method: rpc.MethodStateDelta, Params: json.RawMessage(`{"seq":1}`)}
	event := rpc.Message{Method: rpc.MethodStateEvent, Params: json.RawMessage(`{"kind":"port_up"}`)}

	for i := 0; i < 200; i++ {
		s := &Subscription{
			deltas: make(chan state.Delta, 1),
			events: make(chan state.Event, 1),
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("deliver panicked against a concurrent close: %v", r)
				}
			}()
			for j := 0; j < 50; j++ {
				s.deliver(delta)
				s.deliver(event)
			}
		}()
		go func() {
			defer wg.Done()
			s.close()
		}()
		wg.Wait()

		// close is idempotent, and a delivery after it is a no-op.
		s.close()
		s.deliver(delta)
	}
}

// TestDeliverDropsWhenFull keeps the Dropped() contract: a full channel drops
// the message instead of blocking the read loop.
func TestDeliverDropsWhenFull(t *testing.T) {
	s := &Subscription{deltas: make(chan state.Delta, 1)}
	msg := rpc.Message{Method: rpc.MethodStateDelta, Params: json.RawMessage(`{"seq":1}`)}

	s.deliver(msg)
	if s.Dropped() {
		t.Fatal("a delivery into a free buffer must not count as dropped")
	}
	if len(s.deltas) != 1 {
		t.Fatalf("want 1 buffered delta, got %d", len(s.deltas))
	}

	s.deliver(msg)
	if !s.Dropped() {
		t.Fatal("a delivery into a full buffer must set Dropped")
	}
}

// TestDeliverAfterCloseIsQuiet makes sure a late notification is dropped
// silently rather than panicking or being counted as a dropped message.
func TestDeliverAfterCloseIsQuiet(t *testing.T) {
	s := &Subscription{
		deltas: make(chan state.Delta, 1),
		events: make(chan state.Event, 1),
	}
	s.close()
	s.deliver(rpc.Message{Method: rpc.MethodStateDelta, Params: json.RawMessage(`{"seq":1}`)})
	s.deliver(rpc.Message{Method: rpc.MethodStateEvent, Params: json.RawMessage(`{"kind":"port_up"}`)})
	if s.Dropped() {
		t.Fatal("a notification after close is not a dropped message")
	}
}
