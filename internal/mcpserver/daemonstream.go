package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/raskrebs/sonar/internal/daemon/client"
)

// Stream calls a streaming daemon method (contract §1) and hands back the open
// stream. Only the request/reply half is bounded by the per-call timeout: the
// chunks that follow are the caller's to drain for as long as its own context
// allows, which is what makes a 300-second `ports.wait` possible on a
// connection whose ordinary calls give up after ten.
//
// The caller owns the stream: drain it to its end, Cancel it when the client
// goes away, and Close it when done.
func (d *Daemon) Stream(ctx context.Context, method string, params any) (*client.Stream, error) {
	c := d.current()
	if c == nil {
		return nil, d.unavailable()
	}
	callCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
	defer cancel()

	st, err := c.Stream(callCtx, method, params, nil)
	if err == nil {
		return st, nil
	}
	switch {
	case ctx.Err() != nil:
		// The MCP client went away or cancelled; not a daemon fault.
		return nil, ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return nil, Domain(CodeTimeout,
			fmt.Sprintf("the daemon did not answer %s within %s", method, d.opts.Timeout),
			"the daemon may be scanning a busy machine; retry, or check `sonar daemon status`")
	}
	if _, ok := asDomain(err); ok {
		return nil, err // a daemon error object: pass the contract code through
	}
	d.log.Warn("daemon stream failed on a dropped connection", "method", method, "error", err)
	return nil, d.unavailable()
}
