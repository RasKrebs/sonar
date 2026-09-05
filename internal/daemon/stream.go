package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// streamSeq numbers subscriptions for the life of the process. Ids are unique
// across connections so a log line can name one without also naming a client.
var streamSeq atomic.Uint64

// nextStreamID allocates the id a stream's chunks carry.
func nextStreamID() string { return fmt.Sprintf("sub-%d", streamSeq.Add(1)) }

// Stream is one running streaming method (contract §1): the daemon replies to
// the call with a subscription id, pushes `stream.chunk {id, data}`
// notifications, and finishes with `stream.end {id, data?, error?}`.
//
// A stream ends when its producer returns, when the client sends
// `stream.cancel {id}`, or when the connection drops — the last two both cancel
// the producer's context, so a producer only has to honour ctx.
type Stream struct {
	id   string
	conn *Conn

	ctx    context.Context
	cancel context.CancelFunc

	once sync.Once
}

// ID is the subscription id the client was handed.
func (s *Stream) ID() string { return s.id }

// Context is cancelled by stream.cancel, by the client disconnecting and by
// daemon shutdown. Producers must select on it.
func (s *Stream) Context() context.Context { return s.ctx }

// Done is closed when the stream is cancelled.
func (s *Stream) Done() <-chan struct{} { return s.ctx.Done() }

// Send pushes one chunk. It reports the cancellation cause once the stream is
// over, so a producer that ignores Context still stops on the first Send.
func (s *Stream) Send(data any) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling stream chunk: %w", err)
	}
	return s.conn.Notify(rpc.MethodStreamChunk, rpc.StreamChunk{ID: s.id, Data: raw})
}

// finish deregisters the stream and sends stream.end exactly once. A cancelled
// stream still gets an end notification, without an error: the client asked for
// it, so it is not a failure.
func (s *Stream) finish(data any, err error) {
	s.once.Do(func() {
		s.conn.CancelStream(s.id)
		end := rpc.StreamEnd{ID: s.id}
		if data != nil {
			if raw, mErr := json.Marshal(data); mErr == nil {
				end.Data = raw
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			end.Error = errorFor(err)
		}
		_ = s.conn.Notify(rpc.MethodStreamEnd, end)
	})
}

// StreamFunc produces a stream's chunks. It returns the payload of stream.end
// (nil for a stream with no final result) and an error, which becomes
// stream.end's error object. Returning ctx.Err() after a cancellation is not an
// error and is reported as a clean end.
type StreamFunc func(ctx context.Context, s *Stream) (any, error)

// StartStream turns a handler into a streaming method. It allocates a
// subscription id, replies to the call with `initial` plus that id, registers
// the stream so stream.cancel and disconnect can stop it, and runs produce in
// its own goroutine.
//
// initial must marshal to a JSON object (or be nil, for a bare
// {subscription_id}). The reply is queued before produce starts, so no chunk
// can overtake the id it belongs to.
//
// Handlers return its two values directly: it always reports ErrResponseSent.
func StartStream(ctx context.Context, req *Request, initial any, produce StreamFunc) (any, error) {
	id := nextStreamID()
	result, err := withSubscriptionID(initial, id)
	if err != nil {
		return nil, err
	}

	sctx, cancel := context.WithCancel(ctx)
	s := &Stream{id: id, conn: req.Conn, ctx: sctx, cancel: cancel}

	req.Conn.RegisterStream(id, cancel)
	req.Conn.replyResult(req.ID, result)

	go func() {
		defer cancel()
		end, perr := produce(sctx, s)
		s.finish(end, perr)
	}()
	return nil, ErrResponseSent
}

// withSubscriptionID merges the subscription id into a streaming method's
// initial result. A non-object initial payload is a programming error in a
// handler, not something a client can provoke, so it surfaces as an internal
// error rather than invalid params.
func withSubscriptionID(initial any, id string) (json.RawMessage, error) {
	if initial == nil {
		return json.Marshal(rpc.StreamStart{SubscriptionID: id})
	}
	raw, err := json.Marshal(initial)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal,
			"marshalling stream reply: "+err.Error(), "")
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, rpc.NewError(rpc.CodeInternal,
			fmt.Sprintf("a streaming method must reply with a JSON object, got %s", raw), "")
	}
	fields["subscription_id"], _ = json.Marshal(id)
	return json.Marshal(fields)
}
