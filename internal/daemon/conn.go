package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
)

// QueueSize bounds a connection's outbound queue. A client that stops reading
// fills it and is disconnected rather than blocking the scanner (spec,
// "Subscriptions").
const QueueSize = 256

// MaxMessageBytes is the framing limit from the spec's "Transport details".
const MaxMessageBytes = 4 << 20

// Conn is one client connection: a read loop that dispatches requests and a
// write loop that drains a bounded outbound queue.
type Conn struct {
	id  uint64
	srv *Server
	nc  net.Conn

	out       chan []byte
	closeOnce sync.Once
	closed    chan struct{}
	writerWG  sync.WaitGroup

	// Guarded by srv.subsMu, because the publisher reads them while fanning
	// out and state.subscribe must register atomically with its own reply.
	subscribed bool
	include    scanner.Include
	events     bool
	// hosts is the `state.subscribe {hosts}` filter. The zero value is
	// localhost only, so a client that never sends the field reads exactly the
	// stream it read before remote hosts existed.
	hosts state.HostFilter

	mu             sync.Mutex
	client         string
	clientVersion  string
	keepalive      bool
	helloDone      bool
	streams        map[string]context.CancelFunc
	shutdownOnIdle bool
}

func newConn(id uint64, srv *Server, nc net.Conn) *Conn {
	return &Conn{
		id:      id,
		srv:     srv,
		nc:      nc,
		out:     make(chan []byte, QueueSize),
		closed:  make(chan struct{}),
		streams: map[string]context.CancelFunc{},
	}
}

// ID is the connection's daemon-assigned identifier, used in log lines.
func (c *Conn) ID() uint64 { return c.id }

// Client is the {name, version, keepalive} the client sent with daemon.hello.
func (c *Conn) Client() (name, version string, keepalive bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client, c.clientVersion, c.keepalive
}

// setHello records the client identity. A second hello on the same connection
// simply overwrites it.
func (c *Conn) setHello(name, version string, keepalive bool) {
	c.mu.Lock()
	c.client, c.clientVersion, c.keepalive, c.helloDone = name, version, keepalive, true
	c.mu.Unlock()
	c.srv.recountKeepalive()
}

// Keepalive reports whether this client asked the daemon to stay up.
func (c *Conn) Keepalive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keepalive
}

// RegisterStream records a cancel function for a streaming subscription so
// stream.cancel and disconnect can both stop it (contract §1).
func (c *Conn) RegisterStream(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	c.streams[id] = cancel
	c.mu.Unlock()
}

// CancelStream stops one stream. It reports whether the id was known.
func (c *Conn) CancelStream(id string) bool {
	c.mu.Lock()
	cancel, ok := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// cancelAllStreams stops every stream on this connection. Disconnecting
// cancels all of a connection's streams (contract §1).
func (c *Conn) cancelAllStreams() {
	c.mu.Lock()
	streams := c.streams
	c.streams = map[string]context.CancelFunc{}
	c.mu.Unlock()
	for _, cancel := range streams {
		cancel()
	}
}

// requestShutdown marks the connection so the daemon stops once this request's
// reply has been queued. daemon.shutdown uses it to answer before exiting.
func (c *Conn) requestShutdown() {
	c.mu.Lock()
	c.shutdownOnIdle = true
	c.mu.Unlock()
}

func (c *Conn) shutdownRequested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shutdownOnIdle
}

// enqueue puts one pre-marshalled message on the outbound queue. It never
// blocks: a full queue means the client is not reading, and the connection is
// closed instead (spec, "Subscriptions"). It reports whether the message was
// accepted.
func (c *Conn) enqueue(msg []byte) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.out <- msg:
		return true
	default:
		c.srv.logger.Warn("disconnecting client: outbound queue full",
			"conn", c.id, "queue", QueueSize)
		go c.closeWithReason("outbound queue overflow")
		return false
	}
}

// Notify sends a notification to this client.
func (c *Conn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	msg, err := json.Marshal(rpc.Notification{JSONRPC: rpc.Version, Method: method, Params: raw})
	if err != nil {
		return err
	}
	if !c.enqueue(msg) {
		return errors.New("daemon: connection closed")
	}
	return nil
}

// closeWithReason shuts the connection down once, logging why.
func (c *Conn) closeWithReason(reason string) {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.cancelAllStreams()
		_ = c.nc.Close()
		c.srv.removeConn(c, reason)
	})
}

// Close ends the connection.
func (c *Conn) Close() { c.closeWithReason("closed") }

// writeLoop drains the outbound queue onto the socket. It exits when the
// connection closes; a write error closes the connection.
func (c *Conn) writeLoop() {
	defer c.writerWG.Done()
	w := bufio.NewWriter(c.nc)
	for {
		select {
		case <-c.closed:
			// Best-effort flush of whatever is already queued.
			for {
				select {
				case msg := <-c.out:
					_, _ = w.Write(msg)
					_, _ = w.WriteString("\n")
				default:
					_ = w.Flush()
					return
				}
			}
		case msg := <-c.out:
			if _, err := w.Write(msg); err != nil {
				c.closeWithReason("write error: " + err.Error())
				return
			}
			if _, err := w.WriteString("\n"); err != nil {
				c.closeWithReason("write error: " + err.Error())
				return
			}
			// Coalesce whatever else is already queued into one flush.
			drained := true
			for drained {
				select {
				case more := <-c.out:
					_, _ = w.Write(more)
					_, _ = w.WriteString("\n")
				default:
					drained = false
				}
			}
			if err := w.Flush(); err != nil {
				c.closeWithReason("write error: " + err.Error())
				return
			}
		}
	}
}

// readLoop decodes and dispatches messages until the client disconnects.
// Requests are served one at a time, in order, so a subscribe reply can never
// overtake the deltas that follow it.
func (c *Conn) readLoop(ctx context.Context) {
	dec := rpc.NewDecoder(c.nc, MaxMessageBytes)
	for {
		msg, err := dec.Next()
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				c.closeWithReason("client disconnected")
			case errors.Is(err, rpc.ErrOversize):
				c.replyError(nil, rpc.NewError(rpc.CodeInvalidParams,
					"message exceeds the 4 MiB limit", ""))
				continue
			default:
				var re *rpc.Error
				if errors.As(err, &re) {
					c.replyError(nil, re)
					continue
				}
				c.closeWithReason("read error: " + err.Error())
			}
			return
		}

		switch {
		case msg.IsRequest():
			c.serve(ctx, msg)
		case msg.IsNotification():
			// The daemon accepts no client notifications yet; ignore them
			// rather than closing, so a future client stays compatible.
			c.srv.logger.Debug("ignoring client notification", "conn", c.id, "method", msg.Method)
		default:
			c.replyError(msg.ID, rpc.NewError(rpc.CodeInvalidParams,
				"expected a request with both id and method", ""))
		}

		select {
		case <-c.closed:
			return
		default:
		}
	}
}

// serve runs one request through the dispatcher and writes its reply.
func (c *Conn) serve(ctx context.Context, msg rpc.Message) {
	c.srv.touch()

	h, ok := lookupHandler(msg.Method)
	if !ok {
		c.replyError(msg.ID, rpc.NewError(rpc.CodeNotFound,
			"unknown method "+msg.Method,
			"run `sonar daemon schema` to see the methods this daemon serves"))
		return
	}

	req := &Request{Method: msg.Method, ID: msg.ID, Params: msg.Params, Conn: c, Runtime: c.srv.runtime}
	// serveRequest is h plus the one thing that happens in front of every
	// handler: a request naming a remote host goes over that host's bridge
	// instead of to the handler (hostroute.go).
	result, err := serveRequest(ctx, req, h)
	switch {
	case errors.Is(err, ErrResponseSent):
		// The handler wrote its own reply.
	case err != nil:
		c.replyError(msg.ID, errorFor(err))
	default:
		c.replyResult(msg.ID, result)
	}

	if c.shutdownRequested() {
		go c.srv.Shutdown()
	}
}

// replyResult marshals and queues a successful response.
func (c *Conn) replyResult(id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		c.replyError(id, rpc.NewError(rpc.CodeInternal, "marshalling result: "+err.Error(), ""))
		return
	}
	msg, err := json.Marshal(rpc.Response{JSONRPC: rpc.Version, ID: id, Result: raw})
	if err != nil {
		return
	}
	c.enqueue(msg)
}

// replyError queues an error response. A nil id still gets a response, per
// JSON-RPC, so a client that sent an unparseable request learns why.
func (c *Conn) replyError(id json.RawMessage, e *rpc.Error) {
	if id == nil {
		id = json.RawMessage("null")
	}
	msg, err := json.Marshal(rpc.Response{JSONRPC: rpc.Version, ID: id, Error: e})
	if err != nil {
		return
	}
	c.enqueue(msg)
}
