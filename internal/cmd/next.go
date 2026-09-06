package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	nextConsecutiveFlag int
	nextJSONFlag        bool
	nextClaimFlag       bool
)

var nextCmd = &cobra.Command{
	Use:   "next [port-or-range]",
	Short: "Find the next available (free) TCP port",
	Long: `Find the next available TCP port not currently in use.

By default, searches starting from port 3000. You can specify a start port
or a range (e.g. 3000-3100). Use -n/--consecutive to find multiple consecutive free ports.

Results reflect a point-in-time snapshot; a port could be allocated by another process before you bind it.

Examples:
  sonar next              # first free port starting from 3000
  sonar next 8000         # first free port starting from 8000
  sonar next 3000-3100    # first free port in range 3000-3100
  sonar next -n 3          # find 3 consecutive free ports from 3000
  sonar next 8000 --json  # JSON output
  sonar next --claim      # reserve the port for this worktree, not just report it`,
	Args: cobra.MaximumNArgs(1),
	RunE: nextRun,
}

func init() {
	nextCmd.Flags().IntVarP(&nextConsecutiveFlag, "consecutive", "n", 1, "Number of consecutive free ports to find")
	nextCmd.Flags().BoolVar(&nextJSONFlag, "json", false, "Output as JSON")
	nextCmd.Flags().BoolVar(&nextClaimFlag, "claim", false, "Claim the ports for this project and worktree (needs the daemon)")
	rootCmd.AddCommand(nextCmd)
}

func nextRun(cmd *cobra.Command, args []string) error {
	startPort := 3000
	endPort := 65535

	if nextClaimFlag {
		if len(args) == 1 {
			return fmt.Errorf("--claim derives its own ports; drop the port argument")
		}
		if nextConsecutiveFlag < 1 {
			return fmt.Errorf("--consecutive must be at least 1")
		}
		return nextClaim(cmd)
	}

	if len(args) == 1 {
		arg := args[0]
		if strings.Contains(arg, "-") {
			parts := strings.SplitN(arg, "-", 2)
			lo, err := strconv.Atoi(parts[0])
			if err != nil {
				return fmt.Errorf("invalid range start: %s", parts[0])
			}
			hi, err := strconv.Atoi(parts[1])
			if err != nil {
				return fmt.Errorf("invalid range end: %s", parts[1])
			}
			if lo > hi || lo < 1 || hi > 65535 {
				return fmt.Errorf("invalid port range: %d-%d", lo, hi)
			}
			startPort = lo
			endPort = hi
		} else {
			p, err := strconv.Atoi(arg)
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("invalid port: %s", arg)
			}
			startPort = p
		}
	}

	if nextConsecutiveFlag < 1 {
		return fmt.Errorf("--consecutive must be at least 1")
	}

	freePorts, err := nextFreePorts(cmd.Context(), startPort, endPort, nextConsecutiveFlag)
	if err != nil {
		return err
	}

	if nextJSONFlag {
		out := struct {
			Ports []int `json:"ports"`
		}{Ports: freePorts}
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(out)
	}

	for _, p := range freePorts {
		fmt.Println(p)
	}
	return nil
}

// nextFreePorts asks the daemon, which answers from the shared scan, and falls
// back to scanning here when no daemon is reachable.
func nextFreePorts(ctx context.Context, start, end, count int) ([]int, error) {
	exhausted := func() error {
		if end < 65535 {
			return fmt.Errorf("no %d consecutive free port(s) in range %d-%d", count, start, end)
		}
		return fmt.Errorf("no %d consecutive free port(s) starting from %d", count, start)
	}

	if c := daemonClient(ctx); c != nil {
		defer c.Close()
		var res rpc.PortsNextResult
		err := c.Call(ctx, "ports.next", rpc.PortsNextParams{
			Start: start, End: end, Count: count,
		}, &res)
		var re *rpc.Error
		switch {
		case err == nil:
			return res.Ports, nil
		case errors.As(err, &re) && re.Code == rpc.CodeNotFound:
			return nil, exhausted()
		default:
			return nil, err
		}
	}

	results, err := ports.Scan()
	if err != nil {
		return nil, err
	}
	occupied := make(map[int]bool, len(results))
	for _, r := range results {
		occupied[r.Port] = true
	}
	free := findFreePorts(occupied, start, end, count)
	if len(free) < count {
		return nil, exhausted()
	}
	return free, nil
}

// findFreePorts finds count consecutive free ports in [start, end].
func findFreePorts(occupied map[int]bool, start, end, count int) []int {
	var consecutive []int
	for p := start; p <= end; p++ {
		if occupied[p] {
			consecutive = nil
			continue
		}
		consecutive = append(consecutive, p)
		if len(consecutive) == count {
			return consecutive
		}
	}
	return consecutive
}

// nextClaim is `sonar next --claim`: instead of reporting a port that is free
// right now, reserve it for this project and worktree so the next agent asking
// gets a different one. The ports come from the claim range, not from the
// --start argument, which is why the two cannot be combined.
func nextClaim(cmd *cobra.Command) error {
	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	project, worktree := claimIdentity()
	res, err := acquireClaim(cmd.Context(), c, rpc.ClaimsAcquireParams{
		Project:  project,
		Worktree: worktree,
		Count:    nextConsecutiveFlag,
	})
	if err != nil {
		return err
	}
	return printClaim(project, worktree, res, nextJSONFlag)
}
