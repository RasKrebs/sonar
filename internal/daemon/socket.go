// Package daemon implements `sonar serve`: the socket listener, the JSON-RPC
// dispatcher, the subscription fan-out and the process lifecycle. Handlers for
// namespaces owned by other packages register themselves with RegisterHandler
// from their own init(), so this package never imports them (contract §8).
package daemon

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// WindowsPipe is the named pipe the daemon listens on under Windows
// (contract §7).
const WindowsPipe = `\\.\pipe\sonar`

// SocketPath resolves the address the daemon listens on and every client
// dials, following contract §7:
//
//	$SONAR_SOCKET               if set and non-empty (all platforms)
//	\\.\pipe\sonar              on Windows
//	$XDG_RUNTIME_DIR/sonar/daemon.sock   if that variable is set
//	~/.config/sonar/daemon.sock          otherwise
//
// `sonar daemon path` prints the result so no client hardcodes it.
func SocketPath() string { return socketPathFrom(os.Getenv, os.UserHomeDir, runtime.GOOS) }

// socketPathFrom is SocketPath with its environment injected, so the resolution
// order can be tested across the whole env matrix on one host.
func socketPathFrom(getenv func(string) string, home func() (string, error), goos string) string {
	if s := getenv("SONAR_SOCKET"); s != "" {
		return s
	}
	if goos == "windows" {
		return WindowsPipe
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "sonar", "daemon.sock")
	}
	return filepath.Join(configDirFrom(getenv, home), "daemon.sock")
}

// ConfigDir is ~/.config/sonar: the home of the log file, the lock file when
// the socket lives on a tmpfs, and (from step 1A.4) the database.
func ConfigDir() string { return configDirFrom(os.Getenv, os.UserHomeDir) }

func configDirFrom(getenv func(string) string, home func() (string, error)) string {
	dir, err := home()
	if err != nil || dir == "" {
		// os.UserHomeDir only fails when HOME is unset; fall back to the cwd so
		// the daemon still starts rather than writing to the filesystem root.
		dir = "."
	}
	return filepath.Join(dir, ".config", "sonar")
}

// LogPath is the daemon's rotated log file.
func LogPath() string { return filepath.Join(ConfigDir(), "daemon.log") }

// LockPath is the single-instance lock. It sits beside the socket so that a
// socket on a per-boot tmpfs takes its lock with it.
func LockPath() string { return lockPathFor(SocketPath()) }

func lockPathFor(socket string) string {
	// A named pipe has no directory of its own, so its lock lives with the
	// config. Everything else locks beside the socket.
	if strings.HasPrefix(socket, `\\`) || filepath.Dir(socket) == "." {
		return filepath.Join(ConfigDir(), "daemon.lock")
	}
	return filepath.Join(filepath.Dir(socket), "daemon.lock")
}

// Dial connects to a listening daemon at path. It is the one place clients
// learn whether the address is a unix socket or a named pipe.
func Dial(path string) (net.Conn, error) { return dial(path) }

// Listen binds the daemon's socket at path with the platform's same-user-only
// permissions: a 0700 directory holding a 0600 socket on Unix, a pipe whose
// DACL grants only the current user and SYSTEM on Windows.
func Listen(path string) (net.Listener, error) { return listen(path) }

// SocketAlive reports whether something is accepting connections at path.
func SocketAlive(path string) bool {
	c, err := dial(path)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
