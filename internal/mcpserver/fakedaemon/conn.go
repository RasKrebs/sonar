package fakedaemon

import (
	"encoding/json"
	"errors"
	"net"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// conn is one client connection: the real decoder on the way in, the real
// encoder on the way out. Requests are answered on their own goroutine so a
// slow handler cannot block the others, exactly as the daemon does.
type conn struct {
	f   *Fake
	nc  net.Conn
	enc *rpc.Encoder

	mu     sync.Mutex
	subbed bool
	closed bool
}

func newConn(f *Fake, nc net.Conn) *conn {
	return &conn{f: f, nc: nc, enc: rpc.NewEncoder(nc)}
}

// serve reads requests until the connection closes. Each request is answered
// on its own goroutine and serve does not wait for them: a daemon that dies
// drops whatever it was working on, and a test that stops the fake mid-call
// must not block on the handler it stopped.
func (c *conn) serve() {
	defer c.close()
	dec := rpc.NewDecoder(c.nc, daemon.MaxMessageBytes)
	for {
		msg, err := dec.Next()
		if err != nil {
			return
		}
		if !msg.IsRequest() {
			continue
		}
		go c.answer(msg)
	}
}

func (c *conn) answer(msg rpc.Message) {
	result, err := c.handle(msg.Method, msg.Params)
	resp := rpc.Response{JSONRPC: rpc.Version, ID: msg.ID}
	if err != nil {
		resp.Error = asRPCError(err)
		_ = c.enc.Encode(resp)
		return
	}
	raw, mErr := json.Marshal(result)
	if mErr != nil {
		resp.Error = rpc.NewError(rpc.CodeInternal, "marshalling result: "+mErr.Error(), "")
		_ = c.enc.Encode(resp)
		return
	}
	resp.Result = raw
	_ = c.enc.Encode(resp)
}

// handle routes the two connection-scoped methods here and everything else to
// the fake's handler map.
func (c *conn) handle(method string, params json.RawMessage) (any, error) {
	switch method {
	case "state.subscribe":
		c.f.count(method)
		c.setSubscribed(true)
		return c.f.snapshot(), nil
	case "state.unsubscribe":
		c.f.count(method)
		c.setSubscribed(false)
		return rpc.OKResult{OK: true}, nil
	}
	return c.f.dispatch(method, params)
}

func (c *conn) notify(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		return
	}
	_ = c.enc.Encode(rpc.Notification{JSONRPC: rpc.Version, Method: method, Params: raw})
}

func (c *conn) subscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subbed
}

func (c *conn) setSubscribed(v bool) {
	c.mu.Lock()
	c.subbed = v
	c.mu.Unlock()
}

func (c *conn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.nc.Close()
}

// asRPCError keeps a handler's *rpc.Error verbatim — the fake's error
// vocabulary must equal the contract §2 registry (contract §21) — and reports
// anything else as internal.
func asRPCError(err error) *rpc.Error {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e
	}
	return rpc.NewError(rpc.CodeInternal, err.Error(), "")
}
