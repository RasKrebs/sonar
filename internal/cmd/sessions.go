package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
	"github.com/raskrebs/sonar/internal/display"
	"github.com/raskrebs/sonar/internal/sessions"
	"github.com/raskrebs/sonar/internal/state"
	"github.com/spf13/cobra"
)

var (
	sessionsAllFlag  bool
	sessionsJSONFlag bool
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions [id]",
	Short: "Show the agent sessions that started something",
	Long: `List the coding-agent sessions sonar has attributed runs to.

A session is one agent — Claude Code, Codex, Cursor — identified by
SONAR_SESSION (` + "`<tool>:<id>`" + `) or detected from the environment it
spawns commands in. Active sessions have at least one run still alive;
inactive ones are kept for seven days.

Examples:
  sonar sessions                     # the sessions with something running
  sonar sessions --all               # including the ones that have finished
  sonar sessions claude-code:abc123  # the runs and ports of one session
  sonar list --session abc123        # the ports one session started
  sonar kill --session abc123        # stop everything it started`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSessions,
}

func init() {
	sessionsCmd.Flags().BoolVarP(&sessionsAllFlag, "all", "a", false,
		"Include sessions with nothing running")
	sessionsCmd.Flags().BoolVar(&sessionsJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(sessionsCmd)
}

func runSessions(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, err := sessionsDaemon(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	if len(args) == 1 {
		return inspectSession(cmd.Context(), c, args[0])
	}
	return listSessions(cmd.Context(), c)
}

// sessionsDaemon connects without autostarting. Sessions live in the daemon's
// memory and database, so there is nothing a direct scan could answer with —
// unlike the read commands in contract §20, this one has to say so.
func sessionsDaemon(ctx context.Context) (*client.Client, error) {
	c, err := dialDaemon(ctx)
	if err != nil {
		if errors.Is(err, client.ErrNotRunning) {
			return nil, errors.New("sessions need a running daemon; start one with `sonar serve --detach`")
		}
		return nil, err
	}
	return c, nil
}

func listSessions(ctx context.Context, c *client.Client) error {
	var out rpc.SessionsListResult
	err := c.Call(ctx, "sessions.list", rpc.SessionsListParams{ActiveOnly: !sessionsAllFlag}, &out)
	if err != nil {
		return cliError(err)
	}

	if sessionsJSONFlag {
		return writeJSON(map[string]any{"sessions": nonNil(out.Sessions)})
	}
	if len(out.Sessions) == 0 {
		if sessionsAllFlag {
			fmt.Println("No agent sessions recorded.")
		} else {
			fmt.Println("No active agent sessions. Try --all.")
		}
		return nil
	}

	fmt.Printf("%-26s %-13s %-22s %-20s %-6s %-6s %s\n",
		display.Bold("SESSION"), display.Bold("TOOL"), display.Bold("WORKTREE"),
		display.Bold("BRANCH"), display.Bold("RUNS"), display.Bold("PORTS"), display.Bold("STATE"))
	for _, s := range out.Sessions {
		fmt.Printf("%-26s %-13s %-22s %-20s %-6d %-6d %s\n",
			display.Cyan(s.ID), toolLabel(s.Session), dash(s.Worktree), dash(s.Branch),
			s.Runs, s.Ports, sessionState(s))
	}
	return nil
}

func inspectSession(ctx context.Context, c *client.Client, id string) error {
	var out rpc.SessionsInspectResult
	if err := c.Call(ctx, "sessions.inspect", rpc.SessionsInspectParams{ID: id}, &out); err != nil {
		return cliError(err)
	}
	if sessionsJSONFlag {
		return writeJSON(out)
	}

	s := out.Session
	fmt.Printf("%s %s\n", display.Bold("Session"), display.Cyan(s.ID))
	fmt.Printf("  %-10s %s\n", "tool", toolLabel(s.Session))
	if s.Label != "" {
		fmt.Printf("  %-10s %s\n", "label", s.Label)
	}
	fmt.Printf("  %-10s %s\n", "worktree", dash(s.Worktree))
	fmt.Printf("  %-10s %s\n", "branch", dash(s.Branch))
	fmt.Printf("  %-10s %s\n", "state", sessionState(s))
	if s.FirstSeen != "" {
		fmt.Printf("  %-10s %s\n", "first seen", s.FirstSeen)
	}
	if s.LastSeen != "" {
		fmt.Printf("  %-10s %s\n", "last seen", s.LastSeen)
	}

	fmt.Printf("\n%s\n", display.Bold("Runs"))
	if len(out.Runs) == 0 {
		fmt.Println("  none running")
	}
	for _, r := range out.Runs {
		fmt.Printf("  %-10s %-18s %-14s pid %-7d %s\n",
			r.ID, display.Cyan(r.Group), r.Name, r.PID, portList(r.Ports))
	}

	fmt.Printf("\n%s\n", display.Bold("Ports"))
	if len(out.Ports) == 0 {
		fmt.Println("  none listening")
	}
	for _, p := range out.Ports {
		fmt.Printf("  %-7d %-24s %s\n", p.Port, p.DisplayName, p.URL)
	}
	return nil
}

// toolLabel names the agent, marking a detected session so the reader knows
// sonar inferred it rather than being told (spec 2 §3).
func toolLabel(s state.Session) string {
	tool := s.Tool
	if tool == "" {
		tool = sessions.ToolAgent
	}
	if s.Detected {
		return tool + display.Dim("?")
	}
	return tool
}

func sessionState(s state.SessionRecord) string {
	if s.Active {
		return display.Green("active")
	}
	return display.Dim("inactive")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func nonNil(s []state.SessionRecord) []state.SessionRecord {
	if s == nil {
		return []state.SessionRecord{}
	}
	return s
}

// currentSession resolves the `current` shorthand spec 2 §3 gives
// `--session`: the session this shell is in, read from its own environment.
func currentSession(id string) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(id), "current") {
		return id, nil
	}
	s, ok := sessions.Detect(sessions.Options{})
	if !ok {
		return "", errors.New("no agent session in this environment; pass a session id, or set SONAR_SESSION")
	}
	return s.ID, nil
}
