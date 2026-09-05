package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/raskrebs/sonar/internal/docker"
	"github.com/raskrebs/sonar/internal/groups"
	"github.com/raskrebs/sonar/internal/ports"
	"github.com/spf13/cobra"
)

var (
	initForceFlag  bool
	initDryRunFlag bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a .sonar.yaml for this project from what is running now",
	Long: "Proposes a group name and one service per listening port whose process\n" +
		"works inside this repository, and writes it next to .git. Edit and\n" +
		"commit the result.",
	Args: cobra.NoArgs,
	RunE: initRun,
}

func init() {
	initCmd.Flags().BoolVar(&initForceFlag, "force", false, "Overwrite an existing "+groups.ConfigName)
	initCmd.Flags().BoolVar(&initDryRunFlag, "dry-run", false, "Print the proposed file instead of writing it")
	rootCmd.AddCommand(initCmd)
}

func initRun(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, _, ok := groups.Find(cwd)
	if !ok {
		root = cwd
		fmt.Fprintf(os.Stderr, "note: %s is not inside a git repository; using it as the project root\n", cwd)
	}
	target := filepath.Join(root, groups.ConfigName)

	if _, err := os.Lstat(target); err == nil && !initForceFlag && !initDryRunFlag {
		return fmt.Errorf("%s already exists; pass --force to overwrite it", target)
	}

	results, err := ports.Scan()
	if err != nil {
		return err
	}
	docker.EnrichPorts(results)
	ports.Enrich(results)
	results = excludeApps(results)

	_, index := groups.Attribute(results)
	cfg := groups.Propose(root, results, index)
	data, err := groups.Marshal(cfg)
	if err != nil {
		return err
	}

	if initDryRunFlag {
		fmt.Print(string(data))
		return nil
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: group %s with %d %s\n",
		target, cfg.Name, len(cfg.Services), pluralWord(len(cfg.Services), "service"))
	if len(cfg.Services) == 0 {
		fmt.Fprintln(os.Stderr, "note: nothing was listening inside this project, so no services were proposed")
	}
	return nil
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
