package remote

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// A streaming method on the far side comes back as a stream on this side: the
// reply carries the remote's subscription id, the chunks keep arriving after
// it, and cancelling reaches the remote daemon.

// streamer teaches the fake daemon one streaming method: it replies with a
// subscription id, pushes the chunks a test gives it, and ends when the test
// says so or when the local side cancels.
type streamer struct {
	fake      *fakeDaemon
	id        string
	cancelled chan struct{}
}

func newStreamer(f *fakeDaemon, method, id string) *streamer {
	s := &streamer{fake: f, id: id, cancelled: make(chan struct{}, 1)}
	f.handle(method, func(json.RawMessage) (any, *rpc.Error) {
		return map[string]any{"ok": true, "affected": []string{}, "subscription_id": id}, nil
	})
	f.handle(rpc.MethodStreamCancel, func(params json.RawMessage) (any, *rpc.Error) {
		var p rpc.StreamCancel
		_ = json.Unmarshal(params, &p)
		if p.ID == id {
			select {
			case s.cancelled <- struct{}{}:
			default:
			}
			s.end(map[string]any{"started": []string{}})
		}
		return rpc.OKResult{OK: true}, nil
	})
	return s
}

func (s *streamer) chunk(v any) {
	raw, _ := json.Marshal(v)
	s.notify(rpc.MethodStreamChunk, rpc.StreamChunk{ID: s.id, Data: raw})
}

func (s *streamer) end(v any) {
	raw, _ := json.Marshal(v)
	s.notify(rpc.MethodStreamEnd, rpc.StreamEnd{ID: s.id, Data: raw})
}

func (s *streamer) notify(method string, params any) {
	s.fake.mu.Lock()
	enc := s.fake.enc
	s.fake.mu.Unlock()
	if enc == nil {
		return
	}
	raw, _ := json.Marshal(params)
	_ = enc.Encode(rpc.Notification{JSONRPC: rpc.Version, Method: method, Params: raw})
}

func TestForwardRelaysAStream(t *testing.T) {
	m, fake, _, _ := newTestManager(t, Host{Name: "hetzner", Target: "deploy@box"})
	s := newStreamer(fake, "groups.start", "sub-7")
	fake.waitSubscribed(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	reply, stream, err := m.Forward(ctx, "hetzner", "groups.start", json.RawMessage(`{"name":"api"}`))
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if stream == nil {
		t.Fatal("a reply with a subscription id must come back as a stream")
	}
	var start rpc.GroupsStartResult
	if err := json.Unmarshal(reply, &start); err != nil {
		t.Fatal(err)
	}
	if start.SubscriptionID != "sub-7" {
		t.Errorf("reply = %s, want the remote's subscription id", reply)
	}

	s.chunk(rpc.GroupsStartChunk{Service: "api", PID: 4242})
	select {
	case raw := <-stream.Chunks():
		var chunk rpc.GroupsStartChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.Service != "api" || chunk.PID != 4242 {
			t.Errorf("chunk = %+v", chunk)
		}
	case <-time.After(testTimeout):
		t.Fatal("the chunk never arrived")
	}

	s.end(rpc.GroupsStartEnd{Started: []string{"api"}})
	select {
	case end := <-stream.End():
		if end.Err != nil {
			t.Fatalf("stream ended with %v", end.Err)
		}
		var summary rpc.GroupsStartEnd
		if err := json.Unmarshal(end.Data, &summary); err != nil {
			t.Fatal(err)
		}
		if len(summary.Started) != 1 || summary.Started[0] != "api" {
			t.Errorf("end = %+v", summary)
		}
	case <-time.After(testTimeout):
		t.Fatal("the stream never ended")
	}
}

// Cancelling the relayed stream cancels the remote one: the far side is what
// is doing the work, and it has to stop.
func TestForwardPropagatesCancel(t *testing.T) {
	m, fake, _, _ := newTestManager(t, Host{Name: "hetzner", Target: "deploy@box"})
	s := newStreamer(fake, "ports.logs", "sub-9")
	fake.waitSubscribed(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, stream, err := m.Forward(ctx, "hetzner", "ports.logs", json.RawMessage(`{"port":3000,"follow":true}`))
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if stream == nil {
		t.Fatal("follow:true must come back as a stream")
	}
	if err := stream.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-s.cancelled:
	case <-time.After(testTimeout):
		t.Fatal("the remote daemon never saw the stream.cancel")
	}
	select {
	case <-stream.End():
	case <-time.After(testTimeout):
		t.Fatal("a cancelled stream must still end")
	}
}

// A plain method is not a stream, however hard the relay looks.
func TestForwardOfAPlainMethodHasNoStream(t *testing.T) {
	m, fake, _, _ := newTestManager(t, Host{Name: "hetzner", Target: "deploy@box"})
	fake.handle("ports.list", func(json.RawMessage) (any, *rpc.Error) {
		return rpc.PortsListResult{}, nil
	})
	fake.waitSubscribed(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, stream, err := m.Forward(ctx, "hetzner", "ports.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if stream != nil {
		t.Error("ports.list is not a streaming method")
	}
}

func TestForwardOnAnUnknownHostFails(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	if _, _, err := m.Forward(context.Background(), "nope", "ports.list", nil); err == nil {
		t.Fatal("want an error for an unregistered host")
	}
}
