//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// listen binds the unix socket. The socket itself is always 0600: local,
// same-user only, enforced by the filesystem (spec, "Transport details"). The
// umask is cleared around the bind because a restrictive umask cannot make a
// socket *more* permissive but a permissive one leaves the socket group- and
// world-readable on some systems.
func listen(path string) (net.Listener, error) {
	if err := prepareSocketDir(filepath.Dir(path), OwnsSocketDir(path)); err != nil {
		return nil, err
	}

	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("tightening socket: %w", err)
	}
	return ln, nil
}

// dial connects to the unix socket.
func dial(path string) (net.Conn, error) { return net.Dial("unix", path) }

// removeStaleSocket deletes a socket file left behind by a daemon that died
// without cleaning up. Only ever called once the lock has been acquired, so
// the file provably belongs to no live daemon.
func removeStaleSocket(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// prepareSocketDir makes sure dir exists before the socket is bound. A
// directory sonar owns (the default location) is created 0700 and tightened to
// 0700 even when it was already there. A directory the user pointed
// SONAR_SOCKET at is created 0700 when it is missing, but an existing one is
// left exactly as it is: `SONAR_SOCKET=/tmp/x.sock` must neither fail on
// "chmod /tmp: operation not permitted" nor silently tighten /tmp
// (contract §21). The socket file is 0600 either way, which is what actually
// keeps other users out.
func prepareSocketDir(dir string, ours bool) error {
	existed := true
	if _, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("checking socket directory %s: %w", dir, err)
		}
		existed = false
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}
	if existed && !ours {
		return nil
	}
	// MkdirAll applies the umask, and is a no-op on a directory we own that
	// already exists, so set the mode explicitly.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("tightening socket directory: %w", err)
	}
	return nil
}
