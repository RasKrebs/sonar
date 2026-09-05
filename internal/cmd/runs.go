package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/runs"
	"github.com/spf13/cobra"
)

var runsJSONFlag bool

var runsCmd = &cobra.Command{
	Use:   "runs",
	Short: "List active tagged runs recorded by `sonar run`",
	RunE:  runsRun,
}

func init() {
	runsCmd.Flags().BoolVar(&runsJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(runsCmd)
}

func runsRun(cmd *cobra.Command, args []string) error {
	Hint(cmd, HintRunsToStartList())

	// Load prunes dead pids, so what remains is the set of live tagged runs.
	reg := runs.Load()
	active := reg.Active()
	sort.Slice(active, func(i, j int) bool { return active[i].PID < active[j].PID })

	if runsJSONFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(active)
	}

	if len(active) == 0 {
		fmt.Println("No active tagged runs.")
		return nil
	}

	fmt.Printf("%s   %s   %s   %s\n",
		display.Bold("PID"), display.Bold("ID"), display.Bold("TAG"), display.Bold("CMD"))
	for _, e := range active {
		fmt.Printf("%-6d %-10s %-20s %s\n", e.PID, e.ID, display.Cyan(e.Tag), e.Cmd)
	}
	return nil
}
