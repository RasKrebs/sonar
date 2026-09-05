// Package rpc holds the JSON-RPC 2.0 wire types, the newline-delimited codec,
// the error registry and the method-description registry the protocol schema is
// generated from. It has no daemon logic; every client (CLI, MCP server, the
// desktop app's Rust bridge via the generated schema) speaks this shape.
package rpc

import "encoding/json"

// Version is the JSON-RPC version string carried by every message.
const Version = "2.0"

// ProtocolVersion is the daemon protocol version (contract §7). Additive
// changes bump the minor; the clients in this plan all target major 1.
const ProtocolVersion = "1.0.0"

// Request is a client call. ID is raw so a client can use numbers or strings.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is the reply to a Request. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Notification is a server-initiated message with no reply.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Message is the union the decoder produces: any of the three above. Callers
// classify it with IsRequest / IsResponse / IsNotification.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsNotification reports whether the message has a method but no id.
func (m Message) IsNotification() bool { return m.ID == nil && m.Method != "" }

// IsRequest reports whether the message has both an id and a method.
func (m Message) IsRequest() bool { return m.ID != nil && m.Method != "" }

// IsResponse reports whether the message is a reply (id, no method).
func (m Message) IsResponse() bool { return m.ID != nil && m.Method == "" }

// Streaming and broadcast notification names (contract §1). Every streaming
// method replies with a subscription id and then pushes stream.chunk
// notifications until stream.end; state.subscribe is the exception and keeps
// its own broadcast names.
const (
	MethodStreamChunk  = "stream.chunk"
	MethodStreamEnd    = "stream.end"
	MethodStreamCancel = "stream.cancel"
	MethodStateDelta   = "state.delta"
	MethodStateEvent   = "state.event"
)

// StreamChunk is the payload of a stream.chunk notification.
type StreamChunk struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// StreamEnd is the payload of a stream.end notification. Data carries the
// method's final result (ports.wait's {ready, timed_out}, for example) and is
// absent for streams that have nothing to say at the end. Error is set when the
// stream ended because of a failure.
type StreamEnd struct {
	ID    string          `json:"id"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

// StreamCancel is the params of a stream.cancel call.
type StreamCancel struct {
	ID string `json:"id"`
}

// StreamStart is the minimum every streaming method returns immediately:
// the subscription id the chunks will carry.
type StreamStart struct {
	SubscriptionID string `json:"subscription_id"`
}
