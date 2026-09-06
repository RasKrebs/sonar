//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// pipeBindTimeout bounds the retry below. It is generous on purpose: a restart
// is already waiting on the outgoing daemon's lock, so the only cost of an
// extra second here is a restart that succeeds instead of one that does not.
const pipeBindTimeout = 5 * time.Second

// listen binds the named pipe with a security descriptor that grants full
// access to the current user and to SYSTEM, and to nobody else — the Windows
// equivalent of the 0600 unix socket (spec, "Transport details").
//
// A pipe instance outlives the process that created it by the moment it takes
// Windows to tear the last instance down, so the daemon that replaces another
// one can meet "Access is denied" on a name that is about to be free. That is
// a race, not a refusal: retry it for a few seconds before giving up.
func listen(path string) (net.Listener, error) {
	sddl, err := userOnlySDDL()
	if err != nil {
		return nil, err
	}
	cfg := &winio.PipeConfig{SecurityDescriptor: sddl}
	deadline := time.Now().Add(pipeBindTimeout)
	for {
		ln, err := winio.ListenPipe(path, cfg)
		if err == nil {
			return ln, nil
		}
		if !pipeNameStillBusy(err) || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// pipeNameStillBusy reports whether creating the pipe failed because the
// previous owner has not finished letting go of the name.
func pipeNameStillBusy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_PIPE_BUSY)
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
