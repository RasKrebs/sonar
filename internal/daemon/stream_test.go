package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// unregisterHandler drops a method again. Only tests need it: RegisterHandler
// panics on a duplicate, so a test that installs a probe has to clean up.
func unregisterHandler(method string) {
	handlersMu.Lock()
	delete(handlers, method)
	handlersMu.Unlock()
}

// streamProbe is a streaming method that emits whatever the test pushes onto
// its channel, so the lifecycle can be driven step by step.
type streamProbe struct {
	lines   chan string
	started chan *Stream
	done    chan error
}

func newStreamProbe() *streamProbe {
	return &streamProbe{
		lines:   make(chan string, 4),
		started: make(chan *Stream, 1),
		done:    make(chan error, 1),
	}
}

func (p *streamProbe) handler(end any) Handler {
	return func(ctx context.Context, req *Request) (any, error) {
		return StartStream(ctx, req, map[string]any{"source": "probe"},
			func(ctx context.Context, s *Stream) (any, error) {
				p.started <- s
				for {
					select {
					case <-ctx.Done():
						p.done <- ctx.Err()
						return end, ctx.Err()
					case line, ok := <-p.lines:
						if !ok {
							p.done <- nil
							return end, nil
						}
						if err := s.Send(map[string]string{"line": line}); err != nil {
							p.done <- err
							return end, err
						}
					}
				}
			})
	}
}

// nextNotification reads until a notification with the wanted method arrives.
func (c *testClient) nextNotification(method string) rpc.Message {
	c.t.Helper()
	for {
		m := c.read()
		if m.IsNotification() && m.Method == method {
			return m
		}
	}
}

func TestStreamChunkEndAndInitialReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := newStreamProbe()
	RegisterHandler("test.streamChunks", p.handler(map[string]any{"lines": 2}))
	t.Cleanup(func() { unregisterHandler("test.streamChunks") })

	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var start struct {
		Source         string `json:"source"`
		SubscriptionID string `json:"subscription_id"`
	}
	if e := c.call("test.streamChunks", rpc.Empty{}, &start); e != nil {
		t.Fatalf("starting stream: %v", e)
	}
	if start.Source != "probe" {
		t.Fatalf("initial payload lost: %+v", start)
	}
	if start.SubscriptionID == "" {
		t.Fatal("no subscription_id in the streaming reply")
	}

	p.lines <- "one"
	p.lines <- "two"

	for _, want := range []string{"one", "two"} {
		msg := c.nextNotification(rpc.MethodStreamChunk)
		var chunk rpc.StreamChunk
		if err := json.Unmarshal(msg.Params, &chunk); err != nil {
			t.Fatalf("decoding chunk: %v", err)
		}
		if chunk.ID != start.SubscriptionID {
			t.Fatalf("chunk id = %q, want %q", chunk.ID, start.SubscriptionID)
		}
		var body struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(chunk.Data, &body); err != nil {
			t.Fatalf("decoding chunk data: %v", err)
		}
		if body.Line != want {
			t.Fatalf("chunk line = %q, want %q", body.Line, want)
		}
	}

	close(p.lines)

	msg := c.nextNotification(rpc.MethodStreamEnd)
	var end rpc.StreamEnd
	if err := json.Unmarshal(msg.Params, &end); err != nil {
		t.Fatalf("decoding stream.end: %v", err)
	}
	if end.ID != start.SubscriptionID {
		t.Fatalf("stream.end id = %q, want %q", end.ID, start.SubscriptionID)
	}
	if end.Error != nil {
		t.Fatalf("stream.end carried an error: %v", end.Error)
	}
	if string(end.Data) != `{"lines":2}` {
		t.Fatalf("stream.end data = %s, want the producer's final payload", end.Data)
	}
}

func TestStreamCancelStopsTheProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := newStreamProbe()
	RegisterHandler("test.streamCancel", p.handler(nil))
	t.Cleanup(func() { unregisterHandler("test.streamCancel") })

	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var start rpc.StreamStart
	if e := c.call("test.streamCancel", rpc.Empty{}, &start); e != nil {
		t.Fatalf("starting stream: %v", e)
	}
	<-p.started

	var ok rpc.OKResult
	if e := c.call("stream.cancel", rpc.StreamCancel{ID: start.SubscriptionID}, &ok); e != nil {
		t.Fatalf("stream.cancel: %v", e)
	}
	if !ok.OK {
		t.Fatal("stream.cancel did not acknowledge")
	}

	select {
	case err := <-p.done:
		if err == nil {
			t.Fatal("producer stopped without a cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the stream did not stop the producer")
	}

	msg := c.nextNotification(rpc.MethodStreamEnd)
	var end rpc.StreamEnd
	if err := json.Unmarshal(msg.Params, &end); err != nil {
		t.Fatalf("decoding stream.end: %v", err)
	}
	if end.Error != nil {
		t.Fatalf("a cancelled stream must end cleanly, got %v", end.Error)
	}

	// The id is gone: cancelling twice is a not_found, not a second cancel.
	if e := c.call("stream.cancel", rpc.StreamCancel{ID: start.SubscriptionID}, nil); e == nil {
		t.Fatal("cancelling an ended stream should be not_found")
	}
}

func TestStreamDisconnectCancelsTheProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := newStreamProbe()
	RegisterHandler("test.streamDisconnect", p.handler(nil))
	t.Cleanup(func() { unregisterHandler("test.streamDisconnect") })

	h := newHarness(t, ctx)
	c := h.dial(ctx)

	var start rpc.StreamStart
	if e := c.call("test.streamDisconnect", rpc.Empty{}, &start); e != nil {
		t.Fatalf("starting stream: %v", e)
	}
	s := <-p.started

	c.conn.Close()

	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnecting did not stop the producer")
	}
	if s.Context().Err() == nil {
		t.Fatal("the stream context is still live after a disconnect")
	}
	if s.conn.CancelStream(s.ID()) {
		t.Fatal("the stream is still registered after a disconnect")
	}
}

func TestStreamSendFailsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t, ctx)
	c := h.dial(ctx)
	h.srv.subsMu.RLock()
	var conn *Conn
	for _, cc := range h.srv.conns {
		conn = cc
	}
	h.srv.subsMu.RUnlock()
	_ = c

	sctx, scancel := context.WithCancel(ctx)
	s := &Stream{id: "sub-test", conn: conn, ctx: sctx, cancel: scancel}
	scancel()
	if err := s.Send(map[string]string{"line": "x"}); err == nil {
		t.Fatal("Send on a cancelled stream should fail")
	}
}
