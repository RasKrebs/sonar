package cmd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/client"
	"github.com/spf13/cobra"
)

var daemonStdioNoAutostart bool

var daemonStdioCmd = &cobra.Command{
	Use:   "stdio",
	Short: "Serve the daemon protocol over stdin/stdout",
	Long: `Serve the daemon's JSON-RPC protocol over this process's stdin and stdout.

The framing and the methods are identical to the socket: one JSON message per
line, the same daemon.hello handshake, the same state.subscribe stream. It is
the far end of a remote host's bridge, which the local daemon opens as

  ssh <host> sonar daemon stdio

and then drives with the ordinary client. Nothing new listens anywhere: the
daemon's socket stays 0600 to the SSH user, and this process is the only thing
that reaches it.

It connects to the daemon already running on this machine, starting one with
` + "`sonar serve --detach`" + ` if there is none, so a bridge shares state with
the CLI, the tray and the desktop app on the same host rather than running a
second scanner beside them.`,
	Args: cobra.NoArgs,
	RunE: runDaemonStdio,
}

func init() {
	daemonStdioCmd.Flags().BoolVar(&daemonStdioNoAutostart, "no-autostart", false,
		"Fail instead of starting a daemon when none is running")
	daemonCmd.AddCommand(daemonStdioCmd)
}

func runDaemonStdio(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	socket := daemon.SocketPath()

	conn, err := daemon.Dial(socket)
	if err != nil {
		if daemonStdioNoAutostart {
			return fmt.Errorf("%w (socket %s)", client.ErrNotRunning, socket)
		}
		if err := client.Autostart(cmd.Context(), "", socket); err != nil {
			return err
		}
		conn, err = daemon.Dial(socket)
		if err != nil {
			return fmt.Errorf("%w: started it but could not connect to %s: %v",
				client.ErrNotRunning, socket, err)
		}
	}
	defer conn.Close()

	return pump(conn, os.Stdin, os.Stdout)
}

// pump copies bytes between the pipes and the socket until either side closes.
// It parses nothing: the protocol on the socket is the protocol on the wire,
// so a method this build has never heard of still works across the bridge.
func pump(conn net.Conn, in io.Reader, out io.Writer) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(conn, in)
		// Our stdin ended: tell the daemon so it can finish what it is writing
		// and close, rather than waiting on a peer that is gone.
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = conn.Close()
		}
	}()

	_, err := io.Copy(out, conn)
	<-done
	if err != nil && !isClosedPipe(err) {
		return err
	}
	return nil
}

// isClosedPipe reports whether an error is the ordinary end of a bridge: the
// far side hung up, or our own stdout went away because ssh exited. Neither is
// worth a non-zero exit status.
func isClosedPipe(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE)
}
