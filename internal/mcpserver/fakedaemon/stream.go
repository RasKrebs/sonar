package fakedaemon

import (
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// The fake's half of the streaming convention (contract §1): the reply carries
// a `subscription_id`, `stream.chunk` notifications follow it, and exactly one
// `stream.end` closes it — including when the client cancels, which ends the
// stream with the data it has rather than with an error.
//
// A streaming handler runs after its reply has been written, on the same
// goroutine that answered the request, so a test can drive `ports.wait` and
// change the fixture underneath it without racing the reply.

// StreamHandler produces one stream's chunks and its end payload. Returning a
// *rpc.Error ends the stream with that error; the fake sends the end either
// way, so a caller draining the stream always finishes.
type StreamHandler func(params json.RawMessage, s *Stream) (any, error)

// HandleStream registers a streaming method. It replaces any unary handler
// registered for the same name.
func (f *Fake) HandleStream(method string, h StreamHandler) {
	f.Handle(method, func(json.RawMessage) (any, error) {
		// Unary dispatch is never reached: conn.answer routes a streaming
		// method to the handler below. This entry exists so the method is
		// known to dispatch (and counted) like any other.
		return nil, rpc.NewError(rpc.CodeInternal, method+" is a streaming method", "")
	})
	f.mu.Lock()
	if f.streamHandlers == nil {
		f.streamHandlers = map[string]StreamHandler{}
	}
	f.streamHandlers[method] = h
	f.mu.Unlock()
}

// streamHandler looks up a streaming method.
func (f *Fake) streamHandler(method string) (StreamHandler, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.streamHandlers[method]
	return h, ok
}

// Stream is one open stream, handed to a StreamHandler.
type Stream struct {
	id string
	c  *conn

	once      sync.Once
	cancelled chan struct{}
}

// streamSeq numbers subscription ids the way the daemon does ("sub-<n>",
// contract §20), across every fake in the process so an id is never reused.
var streamSeq atomic.Int64

// ID is the subscription id the client was told.
func (s *Stream) ID() string { return s.id }

// Chunk sends one stream.chunk.
func (s *Stream) Chunk(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.c.notify(rpc.MethodStreamChunk, rpc.StreamChunk{ID: s.id, Data: raw})
}

// Cancelled closes when the client sends stream.cancel. A handler that waits
// should select on it and return the result it has so far.
func (s *Stream) Cancelled() <-chan struct{} { return s.cancelled }

func (s *Stream) cancel() { s.once.Do(func() { close(s.cancelled) }) }

// answerStream replies with the subscription id, runs the handler, and ends
// the stream with what it returned.
func (c *conn) answerStream(msg rpc.Message, h StreamHandler) {
	s := &Stream{
		id:        "sub-" + strconv.FormatInt(streamSeq.Add(1), 10),
		c:         c,
		cancelled: make(chan struct{}),
	}

	c.mu.Lock()
	if c.streams == nil {
		c.streams = map[string]*Stream{}
	}
	c.streams[s.id] = s
	c.mu.Unlock()

	reply, err := json.Marshal(struct {
		SubscriptionID string `json:"subscription_id"`
	}{s.id})
	if err != nil {
		return
	}
	_ = c.enc.Encode(rpc.Response{JSONRPC: rpc.Version, ID: msg.ID, Result: reply})

	data, hErr := h(msg.Params, s)
	end := rpc.StreamEnd{ID: s.id}
	if hErr != nil {
		end.Error = asRPCError(hErr)
	} else if data != nil {
		if raw, mErr := json.Marshal(data); mErr == nil {
			end.Data = raw
		}
	}
	c.notify(rpc.MethodStreamEnd, end)

	c.mu.Lock()
	delete(c.streams, s.id)
	c.mu.Unlock()
}

// cancelStream serves stream.cancel: the stream is told to stop, and its
// handler still sends the final stream.end (contract §20).
func (c *conn) cancelStream(params json.RawMessage) (any, error) {
	var p rpc.StreamCancel
	if err := unmarshal(params, &p); err != nil {
		return nil, err
	}
	c.mu.Lock()
	s, ok := c.streams[p.ID]
	c.mu.Unlock()
	if !ok {
		return nil, rpc.NewError(rpc.CodeNotFound, "unknown subscription "+p.ID, "")
	}
	s.cancel()
	return rpc.OKResult{OK: true}, nil
}
