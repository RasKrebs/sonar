package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	jsonFlag       bool
	filterFlag     string
	sortFlag       string
	allFlag        bool
	columnsFlag    string
	allColumnsFlag bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all processes listening on localhost TCP ports",
	RunE:  listRun,
}

func init() {
	listCmd.Flags().BoolVar(&jsonFlag, "json", false, "Output as JSON")
	listCmd.Flags().StringVar(&filterFlag, "filter", "", "Filter by type: docker, user, system")
	listCmd.Flags().StringVar(&sortFlag, "sort", "port", "Sort by: port, pid, name, type")
	listCmd.Flags().BoolVarP(&allFlag, "all", "a", false, "Include desktop apps (hidden by default)")
	listCmd.Flags().StringVarP(&columnsFlag, "columns", "c", "",
		"Columns to display (comma-separated: "+strings.Join(display.AllColumns, ", ")+")")
	listCmd.Flags().BoolVar(&allColumnsFlag, "all-columns", false, "Display all available columns")
	rootCmd.AddCommand(listCmd)
}

func listRun(cmd *cobra.Command, args []string) error {
	results, err := ports.Scan()
	if err != nil {
		return err
	}

	docker.EnrichPorts(results)
	ports.Enrich(results)

	// Hide desktop apps unless --all is set
	if !allFlag {
		results = excludeApps(results)
	}

	if filterFlag != "" {
		results = display.FilterPorts(results, filterFlag)
	}

	if jsonFlag {
		return display.RenderJSON(os.Stdout, results)
	}

	var columns []string
	if allColumnsFlag {
		columns = display.AllColumns
	} else if columnsFlag != "" {
		columns = parseColumns(columnsFlag)
	}

	display.RenderTable(os.Stdout, results, display.TableOptions{
		SortBy:  sortFlag,
		Columns: columns,
	})
	return nil
}

func parseColumns(s string) []string {
	parts := strings.Split(s, ",")
	var cols []string
	for _, p := range parts {
		c := strings.TrimSpace(strings.ToLower(p))
		if c != "" {
			cols = append(cols, c)
		}
	}
	return cols
}

func excludeApps(pp []ports.ListeningPort) []ports.ListeningPort {
	var result []ports.ListeningPort
	for _, p := range pp {
		if !p.IsApp {
			result = append(result, p)
		}
	}
	return result
}

// ValidateColumns checks that all column names are valid.
func ValidateColumns(cols []string) error {
	valid := make(map[string]bool)
	for _, c := range display.AllColumns {
		valid[c] = true
	}
	for _, c := range cols {
		if !valid[c] {
			return fmt.Errorf("unknown column %q (available: %s)", c, strings.Join(display.AllColumns, ", "))
		}
	}
	return nil
}
