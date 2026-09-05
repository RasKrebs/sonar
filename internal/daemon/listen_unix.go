//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// listen binds the unix socket. The directory is created 0700 and the socket
// itself is 0600: local, same-user only, enforced by the filesystem (spec,
// "Transport details"). The umask is cleared around the bind because a
// restrictive umask cannot make a socket *more* permissive but a permissive
// one leaves the socket group- and world-readable on some systems.
func listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating socket directory: %w", err)
	}
	// MkdirAll is a no-op on an existing directory, so tighten it explicitly.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tightening socket directory: %w", err)
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
