package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// The MCP server's own error codes. Everything else on a tool result is a
// daemon code from the contract §2 registry, passed through unchanged, so an
// agent that learns `not_found` from one tool recognises it from every other.
const (
	// CodeInvalidArguments is the selector and argument rule (spec 1): a tool
	// was called with arguments that cannot address anything.
	CodeInvalidArguments = "invalid_arguments"
	// CodeCapabilityMissing is a tool whose daemon capability is not present.
	// The tool stays registered so the tool list can be cached.
	CodeCapabilityMissing = "capability_missing"
	// CodeDaemonUnavailable is the daemon being unreachable while the server
	// reconnects. It is a domain result, not a protocol error: an agent should
	// retry rather than conclude the tool is broken.
	CodeDaemonUnavailable = "daemon_unavailable"
	// CodeTimeout is the per-call daemon timeout expiring.
	CodeTimeout = "timeout"
)

// DomainError is a failure the model can act on: it is reported as a tool
// result with IsError set, never as a JSON-RPC error (spec 1, "Error model").
// Protocol failures — a daemon that never came up, a malformed frame — are
// returned as plain errors instead and surface as call failures.
type DomainError struct {
	Code    string
	Message string
	Hint    string
}

func (e *DomainError) Error() string {
	if e.Hint == "" {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message + " (" + e.Hint + ")"
}

// Domain builds a DomainError.
func Domain(code, message, hint string) *DomainError {
	return &DomainError{Code: code, Message: message, Hint: hint}
}

// invalidArguments is the selector rule's error.
func invalidArguments(message, hint string) *DomainError {
	return Domain(CodeInvalidArguments, message, hint)
}

// asDomain maps an error from the daemon connection onto a domain error.
// A daemon *rpc.Error carries the contract's {code, detail, hint} and passes
// through unchanged; a dropped connection or an expired per-call timeout
// becomes the server's own code. A cancelled client request is not a domain
// failure — it is returned as-is so the SDK reports the cancellation.
func asDomain(err error) (*DomainError, bool) {
	if err == nil {
		return nil, false
	}
	var domain *DomainError
	if errors.As(err, &domain) {
		return domain, true
	}
	var rerr *rpc.Error
	if errors.As(err, &rerr) {
		code := rerr.Data.Code
		if code == "" {
			code = rpc.CodeName(rerr.Code)
		}
		detail := rerr.Data.Detail
		if detail == "" {
			detail = rerr.Message
		}
		return Domain(code, detail, rerr.Data.Hint), true
	}
	if errors.Is(err, context.Canceled) {
		return nil, false
	}
	return nil, false
}

// errorResult renders a domain failure the way spec 1 fixes it: text
// "<code>: <message>" followed by a hint line, and structured content
// {"error": {code, message, hint}} so a client can branch without parsing
// prose.
func errorResult(e *DomainError) *mcp.CallToolResult {
	text := e.Code + ": " + e.Message
	if e.Hint != "" {
		text += "\n" + e.Hint
	}
	payload := map[string]any{
		"error": map[string]any{
			"code":    e.Code,
			"message": e.Message,
			"hint":    e.Hint,
		},
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: payload,
	}
}

// ErrorPayload is the structured content of a failed tool call. Tests and
// later clients decode into it rather than re-deriving the shape.
type ErrorPayload struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

// DecodeError reads the {"error": ...} payload off a tool result. It reports
// false for a result that is not an error, or whose structured content is
// something else.
func DecodeError(res *mcp.CallToolResult) (ErrorPayload, bool) {
	var out ErrorPayload
	if res == nil || !res.IsError || res.StructuredContent == nil {
		return out, false
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error.Code == "" {
		return out, false
	}
	return out, true
}

// unsupportedValue is the shared wording for an enum argument the model got
// wrong: say what was passed and list what is accepted.
func unsupportedValue(arg, got string, accepted ...string) *DomainError {
	return invalidArguments(
		fmt.Sprintf("unknown %s %q", arg, got),
		fmt.Sprintf("%s accepts %s", arg, strings.Join(quoteAll(accepted), ", ")),
	)
}

func quoteAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = `"` + v + `"`
	}
	return out
}
