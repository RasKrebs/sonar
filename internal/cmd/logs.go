package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	logsFollow    bool
	logsLinesFlag int
)

var logsCmd = &cobra.Command{
	Use:               "logs <port>",
	Short:             "Attach to a process and view its log output",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePort,
	RunE: func(cmd *cobra.Command, args []string) error {
		port, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid port: %s", args[0])
		}

		bindIP, _ := cmd.Flags().GetString("ip")

		if onRemoteHost() {
			// Another machine's output only reaches this terminal through the
			// daemon that holds the bridge; there is no direct path to fall
			// back to.
			c, err := connectForHostWrite(cmd.Context())
			if err != nil {
				return err
			}
			defer c.Close()
			return logsThroughDaemon(cmd.Context(), c, port, bindIP)
		}

		if c := daemonClient(cmd.Context()); c != nil {
			defer c.Close()
			return logsThroughDaemon(cmd.Context(), c, port, bindIP)
		}

		lp, err := ports.FindByPort(port, bindIP)
		if err != nil {
			return err
		}

		// Enrich to get Docker info and full command
		enriched := []ports.ListeningPort{*lp}
		docker.EnrichPorts(enriched)
		ports.Enrich(enriched)
		*lp = enriched[0]

		fmt.Printf("%s %s (PID %s)\n\n",
			display.Dim("Attaching to"),
			display.Bold(lp.DisplayName()),
			display.Cyan(fmt.Sprintf("%d", lp.PID)))

		// Docker containers: use docker logs
		if lp.Type == ports.PortTypeDocker && lp.DockerContainer != "" {
			return execDockerLogs(lp.DockerContainer)
		}

		// Windows: log discovery is not supported
		if runtime.GOOS == "windows" {
			return fmt.Errorf("log viewing is not supported on Windows for non-Docker processes")
		}

		// Regular processes: find log sources via lsof
		sources := ports.FindLogSources(lp.PID)
		if len(sources) > 0 {
			return tailLogSources(sources)
		}

		// Fallback: macOS log stream
		if ports.SupportsLogStream() {
			fmt.Println(display.Dim("No log files found, falling back to system log stream..."))
			fmt.Println()
			return execLogStream(lp.PID)
		}

		// Linux fallback: try /proc/<pid>/fd/1
		return tailProcFD(lp.PID)
	},
}

// logsThroughDaemon tails a port's output over the socket. The daemon owns the
// `tail` (or `docker logs`) process, so several clients watching the same port
// cost one reader, and the lines reach every one of them.
func logsThroughDaemon(ctx context.Context, c *client.Client, port int, bindIP string) error {
	row, err := daemonFindPort(ctx, c, port, bindIP)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s (PID %s)\n\n",
		display.Dim("Attaching to"),
		display.Bold(row.DisplayName()),
		display.Cyan(fmt.Sprintf("%d", row.PID)))

	params := rpc.PortsLogsParams{
		Selector: rpc.Selector{
			HostParams:  hostParams(),
			Port:        &row.Port,
			BindAddress: strPtrOrNil(row.BindAddress),
		},
		Lines:  logsLinesFlag,
		Follow: logsFollow,
	}

	if !logsFollow {
		var res rpc.PortsLogsResult
		if err := c.Call(ctx, "ports.logs", params, &res); err != nil {
			return cliError(err)
		}
		printLogHeader(res.Source)
		for _, line := range res.Lines {
			fmt.Println(line)
		}
		return nil
	}

	var res rpc.PortsLogsResult
	s, err := c.Stream(ctx, "ports.logs", params, &res)
	if err != nil {
		return cliError(err)
	}
	defer s.Close()
	printLogHeader(res.Source)

	for raw := range s.Chunks() {
		var chunk rpc.PortsLogsChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			continue
		}
		fmt.Println(chunk.Line)
	}
	if end := <-s.End(); end.Err != nil {
		return cliError(end.Err)
	}
	return nil
}

// printLogHeader reproduces what the direct path prints above the output: the
// files being tailed, or the note that we fell back to the system log stream.
// A container's logs have never carried a header.
func printLogHeader(source string) {
	switch {
	case source == "" || strings.HasPrefix(source, "docker:"):
		return
	case source == "log stream":
		fmt.Println(display.Dim("No log files found, falling back to system log stream..."))
		fmt.Println()
	default:
		for _, part := range strings.Split(source, ", ") {
			fmt.Println(display.Dim("  " + part))
		}
		fmt.Println()
	}
}

// daemonFindPort resolves one port through ports.list, so the header a command
// prints names the same process the daemon is about to act on.
func daemonFindPort(ctx context.Context, c *client.Client, port int, bindIP string) (*ports.ListeningPort, error) {
	rows, err := hostSnapshot(ctx, c)
	if err != nil {
		return nil, cliError(err)
	}
	var matches []ports.ListeningPort
	for _, row := range rows {
		if row.Port != port {
			continue
		}
		if bindIP != "" && row.BindAddress != bindIP {
			continue
		}
		matches = append(matches, row)
	}
	switch {
	case len(matches) == 0 && bindIP != "":
		return nil, fmt.Errorf("no process found listening on %s:%d", bindIP, port)
	case len(matches) == 0:
		return nil, fmt.Errorf("no process found listening on port %d", port)
	case len(matches) == 1:
		return &matches[0], nil
	}
	addrs := make([]string, 0, len(matches))
	for _, m := range matches {
		addrs = append(addrs, m.BindAddress)
	}
	return nil, fmt.Errorf("port %d is bound to multiple addresses: %s\nUse --ip to specify which one (e.g. --ip %s)",
		port, strings.Join(addrs, ", "), addrs[0])
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", true, "Follow log output (stream continuously)")
	logsCmd.Flags().IntVarP(&logsLinesFlag, "lines", "n", 10, "Number of trailing lines to show before following")
	logsCmd.Flags().String("ip", "", "Specify bind address when a port is bound to multiple IPs")
	addHostFlag(logsCmd, "Tail a port's output on a registered remote `host` instead of this machine")
	rootCmd.AddCommand(logsCmd)
}
