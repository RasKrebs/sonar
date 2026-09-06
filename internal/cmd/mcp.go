package cmd

import (
	"os"
	"os/signal"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/raskrebs/sonar/internal/mcpserver"
	"github.com/raskrebs/sonar/internal/selfupdate"
	"github.com/spf13/cobra"
)

var (
	mcpSocketFlag      string
	mcpLogLevelFlag    string
	mcpNoAutostartFlag bool
	mcpTimeoutFlag     time.Duration
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve sonar to coding agents over MCP (stdio)",
	Long: `Serve sonar's ports, groups and runs to an MCP client over stdio.

This is the server ` + "`sonar install mcp`" + ` registers with Claude Code, Cursor and
Codex; it is not meant to be run by hand, though doing so is harmless — it
speaks JSON-RPC on stdin and stdout and logs to stderr.

It is a client of the sonar daemon and starts one if none is running.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         mcpRun,
}

func init() {
	mcpCmd.Flags().StringVar(&mcpSocketFlag, "socket", "",
		"Daemon socket to connect to (default: the resolved socket, see `sonar daemon path`)")
	mcpCmd.Flags().StringVar(&mcpLogLevelFlag, "log-level", "info",
		"Log level on stderr: debug, info, warn, error, off")
	mcpCmd.Flags().BoolVar(&mcpNoAutostartFlag, "no-autostart", false,
		"Fail instead of starting a daemon when none is running")
	mcpCmd.Flags().DurationVar(&mcpTimeoutFlag, "timeout", mcpserver.DefaultTimeout,
		"How long one daemon call may take")
	rootCmd.AddCommand(mcpCmd)
}

// mcpRun serves the MCP protocol on stdin/stdout. Nothing here may write to
// stdout: it carries the protocol frames, and one stray line makes the client
// drop the connection. That is why the logger is built on stderr and why the
// command returns errSilent — the root command's error print goes to stderr
// too, but the flow is clearer when this one place owns the reporting.
func mcpRun(cmd *cobra.Command, _ []string) error {
	level, enabled, err := mcpserver.ParseLevel(mcpLogLevelFlag)
	if err != nil {
		return err
	}
	log := mcpserver.NewLogger(os.Stderr, level, enabled)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	server, err := mcpserver.New(ctx, mcpserver.Options{
		Version: selfupdate.Version,
		Logger:  log,
		DaemonOptions: mcpserver.DaemonOptions{
			Socket:      mcpSocketFlag,
			NoAutostart: mcpNoAutostartFlag,
			Timeout:     mcpTimeoutFlag,
		},
	})
	if err != nil {
		return err
	}

	log.Info("serving MCP over stdio", "version", selfupdate.Version)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if ctx.Err() != nil {
			return nil // a client that went away, or ^C: not a failure
		}
		return err
	}
	return nil
}
