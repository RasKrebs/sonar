package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/spf13/cobra"
)

var (
	historySince string
	historyLimit int
	historyJSON  bool
)

var historyCmd = &cobra.Command{
	Use:   "history [port]",
	Short: "Show when ports came up, went down and restarted",
	Long: "The daemon records every port transition it publishes. This is that\n" +
		"log, newest first, for one port or for everything.",
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completePort,
	RunE:              historyRun,
}

func init() {
	historyCmd.Flags().StringVar(&historySince, "since", "", "Only events newer than this (24h, 30m, or an RFC 3339 time)")
	historyCmd.Flags().IntVar(&historyLimit, "limit", 0, "Maximum number of events (default 50)")
	historyCmd.Flags().BoolVar(&historyJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(historyCmd)
}

func historyRun(cmd *cobra.Command, args []string) error {
	var params rpc.PortsHistoryParams
	if len(args) == 1 {
		port, err := strconv.Atoi(strings.TrimSpace(args[0]))
		if err != nil || port <= 0 {
			return fmt.Errorf("%q is not a port", args[0])
		}
		params.Port = &port
	}
	if historySince != "" {
		params.Since = &historySince
	}
	params.Limit = historyLimit

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	var res rpc.PortsHistoryResult
	if err := c.Call(cmd.Context(), "ports.history", params, &res); err != nil {
		return daemonError(err)
	}
	if historyJSON {
		return writeJSON(res.Events)
	}
	if len(res.Events) == 0 {
		fmt.Println("no port events recorded yet")
		return nil
	}
	renderHistory(res.Events)
	return nil
}

func renderHistory(events []rpc.HistoryEvent) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
		display.Dim("WHEN"), display.Dim("EVENT"), display.Dim("PORT"),
		display.Dim("PID"), display.Dim("NAME"), display.Dim("GROUP"))
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
			humanTime(e.At), colorKind(e.Kind), e.Port, pidOrDash(e.PID), e.DisplayName, orDash(e.Group))
	}
	_ = w.Flush()
}

// humanTime prints the local wall clock, with the date only when the event is
// not from today.
func humanTime(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	t = t.Local()
	if t.YearDay() == time.Now().YearDay() && t.Year() == time.Now().Year() {
		return t.Format("15:04:05")
	}
	return t.Format("Jan 02 15:04:05")
}

func colorKind(kind string) string {
	switch kind {
	case "port_up":
		return display.Green(kind)
	case "port_down":
		return display.Red(kind)
	case "port_restarted":
		return display.Yellow(kind)
	default:
		return kind
	}
}

func pidOrDash(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
