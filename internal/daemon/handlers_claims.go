package daemon

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/raskrebs/sonar/internal/claims"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/scanner"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/raskrebs/sonar/internal/store"
)

// The claims namespace (spec 2 §4). A claim reserves a port in sonar's
// book-keeping so parallel agents in different worktrees stop picking the same
// one; it is not an OS-level bind, and the handlers say so in their errors.
func init() {
	RegisterHandler("claims.acquire", handleClaimsAcquire)
	RegisterHandler("claims.release", handleClaimsRelease)
	RegisterHandler("claims.list", handleClaimsList)
	RegisterCapability("claims")
}

// Every claims call runs under one lock: the acquire probe reads the table,
// picks ports and writes them back, and two of those interleaving would hand
// the same port to two keys. The calls are rare and short, so a daemon-wide
// mutex is the whole concurrency story. (The unique constraint on
// claims.port is the second line of defence, for a second daemon on the same
// database.)
var claimsMu sync.Mutex

// claimsManager builds the manager for this request, or reports the daemon
// that came up without a database.
func claimsManager(rt *Runtime) (*claims.Manager, error) {
	st := rt.Store
	if st == nil {
		return nil, errNoStore()
	}
	return claims.New(st.Claims(), claims.Options{
		Listening: func() (map[int]bool, error) { return listeningPorts(rt) },
	}), nil
}

// listeningPorts is the live scan as a set, which is how the claim probe skips
// ports something is already serving on.
func listeningPorts(rt *Runtime) (map[int]bool, error) {
	rows, err := readPorts(rt, scanner.Include{})
	if err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(rows))
	for _, row := range rows {
		out[row.Port] = true
	}
	return out, nil
}

func handleClaimsAcquire(_ context.Context, req *Request) (any, error) {
	var p rpc.ClaimsAcquireParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	claimsMu.Lock()
	defer claimsMu.Unlock()
	m, err := claimsManager(req.Runtime)
	if err != nil {
		return nil, err
	}
	res, err := m.Acquire(claims.Request{
		Key:      p.Key,
		Project:  p.Project,
		Worktree: p.Worktree,
		Count:    p.Count,
		TTL:      claimTTL(p),
	})
	if err != nil {
		return nil, claimsError("acquiring a claim", err)
	}
	return rpc.ClaimsAcquireResult{
		MutationResult: rpc.MutationResult{OK: true, Affected: portKeys(res.Ports)},
		Key:            res.Key,
		Ports:          res.Ports,
		ExpiresAt:      res.ExpiresAt.Format(time.RFC3339),
	}, nil
}

func handleClaimsRelease(_ context.Context, req *Request) (any, error) {
	var p rpc.ClaimsReleaseParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if p.Key == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "key is required",
			`release the key claims.acquire returned, e.g. {"key": "sonar/main"}`)
	}
	claimsMu.Lock()
	defer claimsMu.Unlock()
	m, err := claimsManager(req.Runtime)
	if err != nil {
		return nil, err
	}
	n, err := m.Release(p.Key)
	if err != nil {
		return nil, claimsError("releasing a claim", err)
	}
	return rpc.ClaimsReleaseResult{OK: true, Released: n}, nil
}

func handleClaimsList(_ context.Context, req *Request) (any, error) {
	claimsMu.Lock()
	defer claimsMu.Unlock()
	m, err := claimsManager(req.Runtime)
	if err != nil {
		return nil, err
	}
	live, err := m.List()
	if err != nil {
		return nil, claimsError("listing claims", err)
	}
	if live == nil {
		live = []state.Claim{}
	}
	return rpc.ClaimsListResult{Claims: live}, nil
}

// claimTTL reads the spec's ttl_seconds, falling back to the ttl_ms the
// generated schema has always carried. Zero means the manager's default.
func claimTTL(p rpc.ClaimsAcquireParams) time.Duration {
	switch {
	case p.TTLSeconds > 0:
		return time.Duration(p.TTLSeconds) * time.Second
	case p.TTLMs > 0:
		return time.Duration(p.TTLMs) * time.Millisecond
	default:
		return 0
	}
}

func portKeys(ports []int) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, strconv.Itoa(p)+":")
	}
	return out
}

// claimsError maps the manager's failures onto the contract's error codes:
// claim_conflict (1201) for a port another key holds, not_found when the range
// is full, invalid_params for a request that names nothing.
func claimsError(what string, err error) error {
	switch {
	case errors.Is(err, store.ErrClaimed):
		return rpc.NewError(rpc.CodeClaimConflict, err.Error(),
			"another key holds that port; release it or ask for a different count")
	case errors.Is(err, claims.ErrExhausted):
		return rpc.NewError(rpc.CodeNotFound, err.Error(),
			"release stale claims with `sonar claim --release` or widen the range")
	case errors.Is(err, claims.ErrNoProject):
		return rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			`send {"project": "myapp", "worktree": "main"} or {"key": "myapp/main"}`)
	default:
		return storeError(what, err)
	}
}
