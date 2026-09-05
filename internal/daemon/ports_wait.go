package daemon

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
)

// waitDialTimeout bounds one readiness probe, TCP or HTTP.
const waitDialTimeout = 2 * time.Second

// defaultWaitIntervalMs is the poll cadence when the caller does not pick one.
// It matches `sonar wait --interval`'s default.
const defaultWaitIntervalMs = 1000

// probeReady is the readiness check. It is a variable so tests can drive
// ports.wait without opening real sockets.
var probeReady = func(port int, httpPath string) bool {
	if httpPath != "" {
		return ports.ProbeHealth("localhost", port, httpPath, waitDialTimeout).Status == "healthy"
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), waitDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// handlePortsWait streams one chunk per port that becomes ready and ends with
// {ready, timed_out} (contract §4). It is a stream rather than a long call so a
// client can cancel it and so the caller sees progress as it happens.
func handlePortsWait(ctx context.Context, req *Request) (any, error) {
	var p rpc.PortsWaitParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if len(p.Ports) == 0 {
		// run_id selection arrives with the run registry in a later step.
		return nil, rpc.NewError(rpc.CodeInvalidParams, "ports is required",
			`send {"ports": [3000], "timeout_ms": 30000}`)
	}
	for _, port := range p.Ports {
		if port < 1 || port > 65535 {
			return nil, rpc.NewError(rpc.CodeInvalidParams,
				fmt.Sprintf("invalid port %d", port), "")
		}
	}
	if p.TimeoutMs <= 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "timeout_ms must be positive", "")
	}
	interval := time.Duration(p.IntervalMs) * time.Millisecond
	if p.IntervalMs <= 0 {
		interval = defaultWaitIntervalMs * time.Millisecond
	}
	httpPath := ""
	if p.HTTP != nil {
		httpPath = *p.HTTP
	}

	timeout := time.Duration(p.TimeoutMs) * time.Millisecond
	targets := sortedPorts(p.Ports)
	anyReady := p.Any

	return StartStream(ctx, req, nil, func(ctx context.Context, s *Stream) (any, error) {
		pending := map[int]bool{}
		for _, port := range targets {
			pending[port] = true
		}
		ready := []int{}

		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		tick := time.NewTicker(interval)
		defer tick.Stop()

		for {
			for _, port := range targets {
				if !pending[port] || !probeReady(port, httpPath) {
					continue
				}
				delete(pending, port)
				ready = append(ready, port)
				if err := s.Send(rpc.PortsWaitChunk{Port: port, ReadyAt: nowRFC3339()}); err != nil {
					return nil, err
				}
			}
			if len(pending) == 0 || (anyReady && len(ready) > 0) {
				return rpc.PortsWaitEnd{Ready: ready, TimedOut: []int{}}, nil
			}

			select {
			case <-ctx.Done():
				return rpc.PortsWaitEnd{Ready: ready, TimedOut: remaining(targets, pending)}, ctx.Err()
			case <-deadline.C:
				return rpc.PortsWaitEnd{Ready: ready, TimedOut: remaining(targets, pending)}, nil
			case <-tick.C:
			}
		}
	})
}

// remaining lists the still-pending ports in the caller's order.
func remaining(targets []int, pending map[int]bool) []int {
	out := []int{}
	for _, port := range targets {
		if pending[port] {
			out = append(out, port)
		}
	}
	return out
}
