// Package client is the Go client for the sonar daemon. The CLI, `sonar mcp`
// (spec 2) and any other in-process consumer all dial through here, so
// autostart, the protocol version check and the subscription plumbing exist
// once.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// AutostartTimeout is how long a client waits for a daemon it spawned to start
// accepting connections (contract §7).
const AutostartTimeout = 3 * time.Second

// ClientInfo identifies the caller to the daemon in daemon.hello.
type ClientInfo struct {
	// Name is one of cli, app, mcp, tray.
	Name string
	// Version is the caller's own version string.
	Version string
	// Keepalive asks the daemon to disable its idle timeout while connected.
	Keepalive bool
	// Socket overrides the resolved socket path. Empty uses daemon.SocketPath.
	Socket string
	// NoAutostart makes Connect fail instead of spawning `sonar serve -d`.
	NoAutostart bool
	// BinaryPath overrides the executable autostart runs. Empty uses the
	// running executable.
	BinaryPath string
}

// ErrNotRunning is returned when no daemon is listening and autostart was
// disabled or failed.
var ErrNotRunning = errors.New("sonar daemon is not running")

// ProtocolMismatchError is returned when the daemon speaks a different major
// protocol version than this client was built against.
type ProtocolMismatchError struct {
	Daemon string
	Client string
}

func (e *ProtocolMismatchError) Error() string {
	return fmt.Sprintf("sonar daemon speaks protocol v%s, this client needs v%s. Restart it? (`sonar daemon restart`)",
		e.Daemon, e.Client)
}

// Client is a connection to the daemon. It is safe for concurrent use.
type Client struct {
	conn   net.Conn
	enc    *rpc.Encoder
	socket string
	hello  rpc.DaemonHelloResult

	mu       sync.Mutex
	nextID   int64
	pending  map[string]chan rpc.Message
	subs     []*Subscription
	closed   bool
	closeErr error

	done chan struct{}
}

// Connect dials the daemon, autostarting it with `sonar serve --detach` when
// nothing is listening, and completes the daemon.hello handshake.
func Connect(ctx context.Context, info ClientInfo) (*Client, error) {
	socket := info.Socket
	if socket == "" {
		socket = daemon.SocketPath()
	}

	conn, err := daemon.Dial(socket)
	if err != nil {
		if info.NoAutostart {
			return nil, fmt.Errorf("%w (socket %s)", ErrNotRunning, socket)
		}
		if err := Autostart(ctx, info.BinaryPath, socket); err != nil {
			return nil, err
		}
		conn, err = daemon.Dial(socket)
		if err != nil {
			return nil, fmt.Errorf("%w: started it but could not connect to %s: %v",
				ErrNotRunning, socket, err)
		}
	}
	return handshake(ctx, conn, socket, info)
}

// Dial connects to an already-running daemon and never autostarts one.
func Dial(ctx context.Context, info ClientInfo) (*Client, error) {
	info.NoAutostart = true
	return Connect(ctx, info)
}

func handshake(ctx context.Context, conn net.Conn, socket string, info ClientInfo) (*Client, error) {
	c := &Client{
		conn:    conn,
		enc:     rpc.NewEncoder(conn),
		socket:  socket,
		pending: map[string]chan rpc.Message{},
		done:    make(chan struct{}),
	}
	go c.readLoop()

	var hello rpc.DaemonHelloResult
	err := c.Call(ctx, "daemon.hello", rpc.DaemonHelloParams{
		Client:        info.Name,
		ClientVersion: info.Version,
		Keepalive:     info.Keepalive,
	}, &hello)
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := CheckProtocol(hello.ProtocolVersion); err != nil {
		c.Close()
		return nil, err
	}
	c.hello = hello
	return c, nil
}

// CheckProtocol compares a daemon's protocol_version against the one this
// binary was built for. Only the major version has to match: additive changes
// bump the minor (contract §7).
func CheckProtocol(daemonVersion string) error {
	got, err := majorOf(daemonVersion)
	if err != nil {
		return fmt.Errorf("daemon reported an unparseable protocol version %q", daemonVersion)
	}
	want, err := majorOf(rpc.ProtocolVersion)
	if err != nil {
		return err
	}
	if got != want {
		return &ProtocolMismatchError{Daemon: daemonVersion, Client: rpc.ProtocolVersion}
	}
	return nil
}

func majorOf(v string) (int, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	head, _, _ := strings.Cut(v, ".")
	if head == "" {
		return 0, fmt.Errorf("empty version")
	}
	return strconv.Atoi(head)
}

// Hello is the handshake result: protocol and daemon versions, pid, socket and
// capabilities.
func (c *Client) Hello() rpc.DaemonHelloResult { return c.hello }

// Socket is the address this client is connected to.
func (c *Client) Socket() string { return c.socket }

// Call sends a request and decodes the result into out, which may be nil.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.closeErr
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	reply := make(chan rpc.Message, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpc.Request{
		JSONRPC: rpc.Version,
		ID:      json.RawMessage(id),
		Method:  method,
		Params:  raw,
	}
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("sending %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closeErr
	case msg := <-reply:
		if msg.Error != nil {
			return msg.Error
		}
		if out == nil || len(msg.Result) == 0 {
			return nil
		}
		return json.Unmarshal(msg.Result, out)
	}
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage("{}"), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshalling params: %w", err)
	}
	return raw, nil
}

// readLoop demultiplexes responses to pending calls and notifications to
// subscriptions.
func (c *Client) readLoop() {
	dec := rpc.NewDecoder(c.conn, daemon.MaxMessageBytes)
	for {
		msg, err := dec.Next()
		if err != nil {
			c.shutdown(fmt.Errorf("daemon connection closed: %w", err))
			return
		}
		switch {
		case msg.IsResponse():
			c.mu.Lock()
			ch, ok := c.pending[string(msg.ID)]
			c.mu.Unlock()
			if ok {
				ch <- msg
			}
		case msg.IsNotification():
			c.dispatchNotification(msg)
		}
	}
}

func (c *Client) dispatchNotification(msg rpc.Message) {
	c.mu.Lock()
	subs := append([]*Subscription{}, c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		s.deliver(msg)
	}
}

// shutdown closes the client once, failing every pending call.
func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.closeErr = true, err
	subs := c.subs
	c.subs = nil
	c.mu.Unlock()

	close(c.done)
	_ = c.conn.Close()
	for _, s := range subs {
		s.close()
	}
}

// Close disconnects from the daemon.
func (c *Client) Close() error {
	c.shutdown(errors.New("client closed"))
	return nil
}

// Done is closed when the connection drops.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns why the connection closed, or nil while it is open.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}
