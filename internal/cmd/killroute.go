package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/killer"
)

// killThroughDaemon runs `sonar kill` as `ports.kill` / `groups.kill`.
//
// Target selection stays here, against the port table the daemon serves, so
// every message the user can see — the invalid-selector errors, "no listening
// port belongs to group X", the confirmation list, the result lines and the
// --json list — is produced by the same code as the direct path. Only the
// signalling moves: the daemon kills, then rescans and records the port going
// down, which is what the CLI's own killer could never do (contract §22).
func killThroughDaemon(ctx context.Context, c *client.Client, args []string, bindIP string) error {
	snapshot, err := daemonList(ctx, c, rpc.PortsListParams{All: true})
	if err != nil {
		return cliError(err)
	}
	targets, confirm, err := killTargets(args, snapshot, bindIP)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return reportKill(os.Stdout, nil, snapshot, killJSONFlag, killDryRunFlag)
	}

	opts := killOptions()
	call := killCallFor(targets, opts)

	if confirm && !killYesFlag && !killDryRunFlag {
		plan, err := call(ctx, c, true)
		if err != nil {
			return cliError(err)
		}
		if !confirmPlan(plan, snapshot) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	results, err := call(ctx, c, opts.DryRun)
	if err != nil {
		return cliError(err)
	}
	return reportKill(os.Stdout, results, snapshot, killJSONFlag, opts.DryRun)
}

// killCall performs one kill round-trip. dryRun is passed separately because a
// confirmation asks for the plan before asking for the real thing.
type killCall func(ctx context.Context, c *client.Client, dryRun bool) ([]killer.Result, error)

// killCallFor picks the method. `-g` goes to groups.kill so the group is
// resolved by the daemon that owns the snapshot; the flags groups.kill has no
// field for (--tree, --no-escalate) send the group's members to ports.kill
// instead, which supports them and acts on exactly the ports killTargets
// already selected.
func killCallFor(targets []killer.Target, opts killer.Options) killCall {
	if killGroupFlag != "" && !opts.Tree && opts.Escalate == nil {
		name := killGroupFlag
		return func(ctx context.Context, c *client.Client, dryRun bool) ([]killer.Result, error) {
			var env rpc.KillEnvelope
			err := c.Call(ctx, "groups.kill", rpc.GroupsKillParams{
				Name:    name,
				Force:   opts.Force,
				GraceMs: graceMs(opts),
				DryRun:  dryRun,
			}, &env)
			return env.Results, err
		}
	}

	selectors := killSelectors(targets)
	return func(ctx context.Context, c *client.Client, dryRun bool) ([]killer.Result, error) {
		var env rpc.KillEnvelope
		err := c.Call(ctx, "ports.kill", rpc.PortsKillParams{
			Targets:  selectors,
			Tree:     opts.Tree,
			Force:    opts.Force,
			GraceMs:  graceMs(opts),
			Escalate: opts.Escalate,
			DryRun:   dryRun,
		}, &env)
		return env.Results, err
	}
}

// killSelectors turns the targets killTargets resolved into wire selectors. A
// selector sets exactly one of port and pid, which is what the targets already
// are: nothing here re-selects.
func killSelectors(targets []killer.Target) []rpc.Selector {
	out := make([]rpc.Selector, 0, len(targets))
	for _, t := range targets {
		var sel rpc.Selector
		switch {
		case t.ProxyID != "":
			id := t.ProxyID
			sel.ProxyID = &id
		case t.RunID != "":
			id := t.RunID
			sel.RunID = &id
		case t.PID > 0:
			pid := t.PID
			sel.PID = &pid
		default:
			port := t.Port
			sel.Port = &port
			if t.BindAddress != "" {
				bind := t.BindAddress
				sel.BindAddress = &bind
			}
		}
		out = append(out, sel)
	}
	return out
}

// graceMs is --grace on the wire. Zero means "the daemon's default", which is
// the same default the killer applies locally.
func graceMs(opts killer.Options) int {
	if opts.Grace <= 0 {
		return 0
	}
	return int(opts.Grace.Milliseconds())
}
