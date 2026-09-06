package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// StreamBuffer is how many chunks a stream holds before the reader has to catch
// up. A full buffer is not dropped the way a state subscription's is: log lines
// and readiness events are not resendable, so the reader is made to wait
// instead — see deliverChunk.
const StreamBuffer = 256

// End is what a stream finishes with: the method's final payload, and the error
// the daemon reported if it failed.
type End struct {
	// Data is stream.end's payload, or nil for a method with no final result.
	Data json.RawMessage
	// Err is set when the daemon ended the stream with an error.
	Err *rpc.Error
}

// Decode unmarshals the end payload into v.
func (e End) Decode(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, v)
}

// Stream is the client half of the streaming convention (contract §1): the
// reply's subscription id, the chunks that follow, and the end.
type Stream struct {
	c  *Client
	id string

	chunks chan json.RawMessage
	end    chan End

	wake      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	queue  []json.RawMessage
	ended  bool
	endVal End

	// claimed is guarded by the client's own mutex, not this one: it is part
	// of the registry's bookkeeping, not the stream's.
	claimed bool
}

// finished reports whether the stream has seen its end.
func (s *Stream) finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// ID is the subscription id the daemon assigned.
func (s *Stream) ID() string { return s.id }

// Chunks carries every stream.chunk payload. It is closed when the stream ends.
func (s *Stream) Chunks() <-chan json.RawMessage { return s.chunks }

// End carries the single stream.end. It is closed after that, and also when the
// connection drops without one.
func (s *Stream) End() <-chan End { return s.end }

// Cancel asks the daemon to stop the stream. The daemon still sends a final
// stream.end, so a caller that wants to drain should keep reading until End
// closes.
func (s *Stream) Cancel(ctx context.Context) error {
	err := s.c.Call(ctx, rpc.MethodStreamCancel, rpc.StreamCancel{ID: s.id}, nil)
	// A stream that ended on its own is already gone; that is not a failure.
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == rpc.CodeNotFound {
		return nil
	}
	return err
}

// Stream calls a streaming method. It sends the request, decodes the reply into
// result (which may be nil) and returns a Stream carrying the chunks that
// follow. The daemon queues the reply before the first chunk, and chunks that
// arrive while the reply is still being decoded are buffered against the
// subscription id, so nothing is lost in the gap.
func (c *Client) Stream(ctx context.Context, method string, params, result any) (*Stream, error) {
	raw, st, err := c.CallStream(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("daemon: " + method + " did not return a subscription_id")
	}
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// CallStream calls a method without knowing whether it streams. It returns the
// raw reply and, when that reply carried a subscription id, the stream its
// chunks are arriving on; a plain method answers with a nil stream.
//
// It exists for the remote-host bridge, which forwards whatever method a client
// asked for and has to relay the chunks if there are any. Registering the
// stream here rather than after inspecting the reply is what keeps a chunk that
// overtakes the caller from being lost: the id is claimed against whatever the
// read loop has already buffered under it.
func (c *Client) CallStream(ctx context.Context, method string, params any) (json.RawMessage, *Stream, error) {
	raw := json.RawMessage(nil)
	if err := c.Call(ctx, method, params, &raw); err != nil {
		return nil, nil, err
	}
	var reply struct {
		SubscriptionID string `json:"subscription_id"`
	}
	// A result that is not an object cannot be a stream reply, and that is not
	// an error: plenty of methods answer with a bare value.
	if err := json.Unmarshal(raw, &reply); err != nil || reply.SubscriptionID == "" {
		return raw, nil, nil
	}

	st := c.streamFor(reply.SubscriptionID)
	st.c, st.id = c, reply.SubscriptionID
	c.claim(reply.SubscriptionID)
	return raw, st, nil
}

// claim marks a stream as handed to its caller. A stream that has already
// ended can now be dropped from the registry; one that has not stays, so the
// chunks still to come find it.
func (c *Client) claim(id string) {
	c.mu.Lock()
	if s, ok := c.streams[id]; ok {
		s.claimed = true
	}
	c.mu.Unlock()
	c.retire(id)
}

// retire forgets a stream once it has both ended and been claimed.
//
// Both halves matter. Forgetting on the end alone loses a stream that finished
// before its own reply was decoded — `groups.start` on a group where every
// service is already running answers in microseconds — and the caller would
// then be handed a fresh, empty stream that never ends. Forgetting on the
// claim alone would orphan the chunks still in flight.
func (c *Client) retire(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.streams[id]
	if ok && s.claimed && s.finished() {
		delete(c.streams, id)
	}
}

// streamFor returns the stream registered under id, creating it if the first
// notification has not arrived yet (or arrived before the reply was decoded).
func (c *Client) streamFor(id string) *Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streams == nil {
		c.streams = map[string]*Stream{}
	}
	if s, ok := c.streams[id]; ok {
		return s
	}
	s := &Stream{
		id:     id,
		chunks: make(chan json.RawMessage, StreamBuffer),
		end:    make(chan End, 1),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	c.streams[id] = s
	go s.pump()
	return s
}

// dispatchStream routes one stream.chunk or stream.end notification. It reports
// whether the message belonged to a stream at all.
func (c *Client) dispatchStream(msg rpc.Message) bool {
	switch msg.Method {
	case rpc.MethodStreamChunk:
		var chunk rpc.StreamChunk
		if err := json.Unmarshal(msg.Params, &chunk); err != nil || chunk.ID == "" {
			return true
		}
		c.streamFor(chunk.ID).enqueue(chunk.Data)
		return true
	case rpc.MethodStreamEnd:
		var end rpc.StreamEnd
		if err := json.Unmarshal(msg.Params, &end); err != nil || end.ID == "" {
			return true
		}
		s := c.streamFor(end.ID)
		s.finish(End{Data: end.Data, Err: end.Error})
		c.retire(end.ID)
		return true
	}
	return false
}

// enqueue queues one chunk. It never blocks the connection's read loop: a slow
// consumer would otherwise stall every other call on the socket, including the
// stream.cancel that would have freed it. Chunks are queued rather than
// dropped, because a log line the daemon has already read cannot be resent.
func (s *Stream) enqueue(data json.RawMessage) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, data)
	s.mu.Unlock()
	s.signal()
}

// finish records the end. The pump delivers it once the queue has drained, so
// a chunk is never lost to a fast end.
func (s *Stream) finish(e End) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended, s.endVal = true, e
	s.mu.Unlock()
	s.signal()
}

func (s *Stream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Close abandons a stream the caller is no longer draining, releasing the pump.
// Cancelling is the polite version; this one only cleans up locally.
func (s *Stream) Close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// pump moves queued chunks onto the channel and delivers the end last.
func (s *Stream) pump() {
	defer s.Close()
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			item := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			select {
			case s.chunks <- item:
			case <-s.done:
				return
			}
			continue
		}
		ended, e := s.ended, s.endVal
		s.mu.Unlock()
		if ended {
			close(s.chunks)
			s.end <- e
			close(s.end)
			return
		}
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
	}
}

// closeStreams ends every open stream when the connection drops.
func (c *Client) closeStreams(err error) {
	c.mu.Lock()
	streams := c.streams
	c.streams = nil
	c.mu.Unlock()
	for _, s := range streams {
		s.finish(End{Err: rpc.NewError(rpc.CodeInternal, err.Error(), "")})
	}
}
