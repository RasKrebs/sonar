package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/notify"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/spf13/cobra"
)

var intervalFlag time.Duration
var notifyFlag bool
var watchStatsFlag bool

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch for port changes in real-time",
	RunE: func(cmd *cobra.Command, args []string) error {
		showAll, _ := cmd.Flags().GetBool("all")
		notifyFlag, _ = cmd.Flags().GetBool("notify")
		watchStatsFlag, _ = cmd.Flags().GetBool("stats")
		watchHost, _ := cmd.Flags().GetString("host")

		statsColumns := append(display.DefaultColumns, "cpu", "mem", "state", "uptime", "connections")

		if watchHost == "" {
			if c := daemonClient(cmd.Context()); c != nil {
				defer c.Close()
				return watchThroughDaemon(cmd.Context(), c, showAll, statsColumns)
			}
		}

		// Initial scan
		current, err := scanAndEnrichWithHost(watchHost, watchStatsFlag)
		if err != nil {
			return err
		}
		if !showAll {
			current = excludeApps(current)
		}

		if watchStatsFlag {
			// Live stats mode: render full table, then update in-place
			fmt.Print("\033[?25l")         // hide cursor
			defer fmt.Print("\033[?25h\n") // show cursor on exit
			renderLiveTable(current, statsColumns)
		} else {
			var columns []string
			display.RenderTable(os.Stdout, current, display.TableOptions{Columns: columns})
			fmt.Println()
			fmt.Println(display.Dim("Watching for changes... (Ctrl+C to stop)"))
			fmt.Println()
		}

		// Set up signal handling
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)

		ticker := time.NewTicker(intervalFlag)
		defer ticker.Stop()

		for {
			select {
			case <-sigCh:
				return nil
			case <-ticker.C:
				next, err := scanAndEnrichWithHost(watchHost, watchStatsFlag)
				if err != nil {
					continue
				}
				if !showAll {
					next = excludeApps(next)
				}
				if watchStatsFlag {
					renderLiveTable(next, statsColumns)
				} else {
					printDiff(current, next)
				}
				current = next
			}
		}
	},
}

func init() {
	watchCmd.Flags().DurationVarP(&intervalFlag, "interval", "i", 2*time.Second, "Poll interval (e.g. 2s, 500ms)")
	watchCmd.Flags().BoolP("all", "a", false, "Include desktop apps (hidden by default)")
	watchCmd.Flags().BoolP("notify", "n", false, "Send desktop notifications on port changes")
	watchCmd.Flags().Bool("stats", false, "Show live resource stats (CPU, memory, state)")
	watchCmd.Flags().String("host", "", "Watch a remote host via SSH (e.g. user@hostname)")
	rootCmd.AddCommand(watchCmd)
}

// watchThroughDaemon subscribes instead of polling: the daemon runs one scan
// for every watcher on the machine, so a second `sonar watch` costs nothing.
// What it prints is what the polling loop prints.
func watchThroughDaemon(ctx context.Context, c *client.Client, showAll bool, statsColumns []string) error {
	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Stats: watchStatsFlag})
	if err != nil {
		return cliError(err)
	}
	defer sub.Unsubscribe(context.WithoutCancel(ctx))

	live := map[string]state.Port{}
	for _, p := range sub.Snapshot.Ports {
		live[p.Key()] = p
	}
	current := watchRows(live, showAll)

	if watchStatsFlag {
		fmt.Print("\033[?25l")         // hide cursor
		defer fmt.Print("\033[?25h\n") // show cursor on exit
		renderLiveTable(current, statsColumns)
	} else {
		display.RenderTable(os.Stdout, current, display.TableOptions{})
		fmt.Println()
		fmt.Println(display.Dim("Watching for changes... (Ctrl+C to stop)"))
		fmt.Println()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-sigCh:
			return nil
		case <-c.Done():
			return nil
		case delta, ok := <-sub.Deltas:
			if !ok {
				return nil
			}
			for _, key := range delta.Ports.Removed {
				delete(live, key)
			}
			for _, p := range delta.Ports.Added {
				live[p.Key()] = p
			}
			for _, p := range delta.Ports.Updated {
				live[p.Key()] = p
			}
			next := watchRows(live, showAll)
			if watchStatsFlag {
				renderLiveTable(next, statsColumns)
			} else {
				printDiff(current, next)
			}
			current = next
		}
	}
}

// watchRows turns the live map back into the rows the renderers take, in a
// stable order so the diff lines do not shuffle between ticks.
func watchRows(live map[string]state.Port, showAll bool) []ports.ListeningPort {
	rows := make([]state.Port, 0, len(live))
	for _, p := range live {
		rows = append(rows, p)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Port != rows[j].Port {
			return rows[i].Port < rows[j].Port
		}
		return rows[i].BindAddress < rows[j].BindAddress
	})
	out := state.ToListeningAll(rows)
	if !showAll {
		out = excludeApps(out)
	}
	return out
}

func scanAndEnrich(withStats bool) ([]ports.ListeningPort, error) {
	results, err := ports.Scan()
	if err != nil {
		return nil, err
	}
	docker.EnrichPorts(results)
	ports.Enrich(results)
	if withStats {
		ports.EnrichStats(results, docker.AllContainerStatsAsEntries())
	}
	// Fill in the GROUP column the table shows by default.
	groups.Attribute(results)
	return results, nil
}

func scanAndEnrichWithHost(host string, withStats bool) ([]ports.ListeningPort, error) {
	if host == "" {
		return scanAndEnrich(withStats)
	}
	results, err := ports.ScanRemote(host)
	if err != nil {
		return nil, err
	}
	for i := range results {
		results[i].Type = ports.ClassifyPort(results[i].Port)
	}
	return results, nil
}

// renderLiveTable renders the table by moving cursor to top-left and overwriting.
// prevLines tracks how many lines were written last time so we can clear stale lines.
var prevLines int

func renderLiveTable(pp []ports.ListeningPort, columns []string) {
	enableVTProcessing()
	if prevLines > 0 {
		// Move cursor up to the beginning of the previous output
		fmt.Printf("\033[%dA\r", prevLines)
	}

	var buf strings.Builder
	display.RenderTable(&buf, pp, display.TableOptions{Columns: columns})
	buf.WriteString("\n")
	buf.WriteString(display.Dim(fmt.Sprintf("Live stats  %s — Ctrl+C to stop", time.Now().Format("15:04:05"))))
	buf.WriteString("\n")

	lines := strings.Count(buf.String(), "\n")

	// Clear any extra lines from previous render
	output := buf.String()
	if prevLines > lines {
		for range prevLines - lines {
			output += "\033[2K\n" // clear line
		}
		// Move back up to end of current output
		output += fmt.Sprintf("\033[%dA", prevLines-lines)
	}

	fmt.Print(output)
	prevLines = lines
}

func printDiff(old, new []ports.ListeningPort) {
	oldMap := make(map[string]ports.ListeningPort)
	for _, p := range old {
		oldMap[p.PortKey()] = p
	}

	newMap := make(map[string]ports.ListeningPort)
	for _, p := range new {
		newMap[p.PortKey()] = p
	}

	now := time.Now().Format("15:04:05")

	// New ports
	for _, p := range new {
		if _, exists := oldMap[p.PortKey()]; !exists {
			fmt.Printf("%s %s %-5d  %-20s  %s\n",
				display.Dim("["+now+"]"),
				display.Green("+ "+fmt.Sprintf("%-5d", p.Port)),
				p.PID,
				p.DisplayName(),
				display.Underline(p.URL()))
			if notifyFlag {
				notify.Send("Port Opened", fmt.Sprintf("Port %d opened (%s on %s)", p.Port, p.DisplayName(), p.BindAddress))
			}
		}
	}

	// Removed ports
	for _, p := range old {
		if _, exists := newMap[p.PortKey()]; !exists {
			fmt.Printf("%s %s %-5d  %-20s  %s\n",
				display.Dim("["+now+"]"),
				display.Red("- "+fmt.Sprintf("%-5d", p.Port)),
				p.PID,
				p.DisplayName(),
				display.Dim(p.URL()))
			if notifyFlag {
				notify.Send("Port Closed", fmt.Sprintf("Port %d closed (%s on %s)", p.Port, p.DisplayName(), p.BindAddress))
			}
		}
	}
}
