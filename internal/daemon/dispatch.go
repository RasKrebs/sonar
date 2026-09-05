package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// Request is what a Handler receives: the decoded call plus the connection it
// arrived on and the daemon runtime it may act through.
type Request struct {
	Method  string
	ID      json.RawMessage
	Params  json.RawMessage
	Conn    *Conn
	Runtime *Runtime
}

// Bind unmarshals the request's params into v. Absent params are left as v's
// zero value, so a method whose params are all optional needs no special case.
func (r *Request) Bind(v any) error {
	if len(r.Params) == 0 || string(r.Params) == "null" {
		return nil
	}
	if err := json.Unmarshal(r.Params, v); err != nil {
		return rpc.NewError(rpc.CodeInvalidParams,
			fmt.Sprintf("invalid params for %s: %v", r.Method, err), "")
	}
	return nil
}

// Handler serves one method. Returning ErrResponseSent means the handler has
// already written the reply itself (state.subscribe does, so that its snapshot
// is queued atomically with the subscription).
type Handler func(ctx context.Context, req *Request) (any, error)

// ErrResponseSent tells the dispatcher not to write a reply.
var ErrResponseSent = errors.New("daemon: response already sent")

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handler{}
)

// RegisterHandler adds a method to the dispatcher. Packages that own a
// namespace (groups, killer, store, expose, sessions) call it from their own
// init() so this package never imports them (contract §8). Registering the
// same method twice panics: two owners for one method is always a bug.
func RegisterHandler(method string, h Handler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if _, dup := handlers[method]; dup {
		panic("daemon: duplicate handler for method " + method)
	}
	handlers[method] = h
}

// lookupHandler returns the handler for a method.
func lookupHandler(method string) (Handler, bool) {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	h, ok := handlers[method]
	return h, ok
}

// RegisteredMethods lists every method this build serves, sorted.
func RegisteredMethods() []string {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	out := make([]string, 0, len(handlers))
	for m := range handlers {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// errorFor converts a handler error into a JSON-RPC error object. Anything that
// is not already an *rpc.Error becomes an internal error, so a handler bug
// never leaks a bare Go error string as a protocol code.
func errorFor(err error) *rpc.Error {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re
	}
	return rpc.NewError(rpc.CodeInternal, err.Error(), "")
}
