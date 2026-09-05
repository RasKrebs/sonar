//go:build windows

package daemon

import (
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// listen binds the named pipe with a security descriptor that grants full
// access to the current user and to SYSTEM, and to nobody else — the Windows
// equivalent of the 0600 unix socket (spec, "Transport details").
func listen(path string) (net.Listener, error) {
	sddl, err := userOnlySDDL()
	if err != nil {
		return nil, err
	}
	return winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: sddl})
}

// dial connects to the named pipe. The short timeout matters because a pipe
// with no server returns immediately rather than blocking.
func dial(path string) (net.Conn, error) {
	timeout := 2 * time.Second
	return winio.DialPipe(path, &timeout)
}

// removeStaleSocket is a no-op on Windows: a named pipe disappears with the
// process that created it, so there is nothing stale to clean up.
func removeStaleSocket(string) error { return nil }

// userOnlySDDL builds "D:P(A;;GA;;;<user SID>)(A;;GA;;;SY)": a protected DACL
// granting generic-all to the daemon's own user and to LocalSystem.
func userOnlySDDL() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("reading process token: %w", err)
	}
	sid := user.User.Sid.String()
	if sid == "" {
		return "", fmt.Errorf("process token has no user SID")
	}
	return fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)", sid), nil
}
