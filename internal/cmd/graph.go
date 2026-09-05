package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	graphJSONFlag bool
	graphDotFlag  bool
)

var graphCmd = &cobra.Command{
	Use:   "graph",
	Short: "Show dependency graph of connected services",
	Long:  "Discover which listening services connect to each other via established TCP connections.",
	RunE:  graphRun,
}

func init() {
	graphCmd.Flags().BoolVar(&graphJSONFlag, "json", false, "Output as JSON")
	graphCmd.Flags().BoolVar(&graphDotFlag, "dot", false, "Output in Graphviz DOT format")
	rootCmd.AddCommand(graphCmd)
}

func graphRun(cmd *cobra.Command, args []string) error {
	connections, err := graphConnections(cmd.Context())
	if err != nil {
		return err
	}

	if len(connections) == 0 {
		fmt.Println("No inter-service connections detected.")
		return nil
	}

	if graphJSONFlag {
		return renderGraphJSON(connections)
	}

	if graphDotFlag {
		return renderGraphDot(connections)
	}

	return renderGraphASCII(connections)
}

// graphConnections asks the daemon for the connection graph, so two clients
// looking at the same machine share one `lsof` pass, and falls back to running
// it here when no daemon is reachable.
func graphConnections(ctx context.Context) ([]ports.Connection, error) {
	if c := daemonClient(ctx); c != nil {
		defer c.Close()
		var res rpc.PortsGraphResult
		if err := c.Call(ctx, "ports.graph", rpc.Empty{}, &res); err != nil {
			return nil, err
		}
		out := make([]ports.Connection, 0, len(res.Connections))
		for _, e := range res.Connections {
			out = append(out, ports.Connection{
				FromPort:    e.FromPort,
				FromProcess: e.FromProcess,
				ToPort:      e.ToPort,
				ToProcess:   e.ToProcess,
			})
		}
		return out, nil
	}

	results, err := ports.Scan()
	if err != nil {
		return nil, err
	}
	docker.EnrichPorts(results)
	ports.Enrich(results)

	connections, err := ports.BuildGraph(results)
	if err != nil {
		return nil, err
	}
	dockerConns, err := docker.BuildDockerGraph(results)
	if err != nil {
		return nil, err
	}
	return append(connections, dockerConns...), nil
}

func renderGraphASCII(connections []ports.Connection) error {
	for _, c := range connections {
		fmt.Printf("%s (%d) \u2192 %s (%d)\n", c.FromProcess, c.FromPort, c.ToProcess, c.ToPort)
	}
	return nil
}

func renderGraphJSON(connections []ports.Connection) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(connections)
}

func renderGraphDot(connections []ports.Connection) error {
	fmt.Println("digraph sonar {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [shape=box, style=rounded];")

	// Collect unique nodes
	nodes := make(map[string]bool)
	for _, c := range connections {
		from := fmt.Sprintf("%s:%d", c.FromProcess, c.FromPort)
		to := fmt.Sprintf("%s:%d", c.ToProcess, c.ToPort)
		nodes[from] = true
		nodes[to] = true
	}

	// Declare nodes
	for n := range nodes {
		fmt.Printf("  %q;\n", n)
	}

	fmt.Println()

	// Declare edges
	for _, c := range connections {
		from := fmt.Sprintf("%s:%d", c.FromProcess, c.FromPort)
		to := fmt.Sprintf("%s:%d", c.ToProcess, c.ToPort)
		fmt.Printf("  %q -> %q;\n", from, to)
	}

	fmt.Println("}")
	return nil
}
