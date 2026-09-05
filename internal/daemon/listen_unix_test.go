//go:build !windows

package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareSocketDirLeavesAUserDirectoryAlone is the smoke-test regression:
// `SONAR_SOCKET=/tmp/x.sock` used to fail with "chmod /tmp: operation not
// permitted", and on a directory it could chmod it silently tightened someone
// else's directory to 0700.
func TestPrepareSocketDirLeavesAUserDirectoryAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("setting up a 0755 directory: %v", err)
	}

	if err := prepareSocketDir(dir, false); err != nil {
		t.Fatalf("prepareSocketDir on a user-supplied directory: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("a user-supplied directory was chmod'ed to %o, want it untouched at 0755", got)
	}
}

// TestPrepareSocketDirCreatesAMissingUserDirectory: a directory that is not
// there yet is ours to make, so it is created 0700.
func TestPrepareSocketDirCreatesAMissingUserDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sock")

	if err := prepareSocketDir(dir, false); err != nil {
		t.Fatalf("prepareSocketDir on a missing directory: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the socket directory was not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("a directory we created has mode %o, want 0700", got)
	}
}

// TestPrepareSocketDirTightensOurOwnDirectory: the default location is sonar's,
// so it is tightened even when it already existed too loose.
func TestPrepareSocketDirTightensOurOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := prepareSocketDir(dir, true); err != nil {
		t.Fatalf("prepareSocketDir on our own directory: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("our own socket directory has mode %o, want 0700", got)
	}
}

// TestListenOnAUserSuppliedSocketPath binds a bare socket in a directory the
// daemon does not own — the `SONAR_SOCKET=/tmp/x.sock` shape — and checks the
// socket file is still 0600.
func TestListenOnAUserSuppliedSocketPath(t *testing.T) {
	dir, err := os.MkdirTemp("", "snr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "d.sock")

	ln, err := listen(socket)
	if err != nil {
		t.Fatalf("listen on a user-supplied socket path: %v", err)
	}
	defer ln.Close()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 0600", got)
	}
	if dirInfo, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Errorf("listen tightened the user's directory to %o, want 0755", got)
	}

	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dialling the socket we just bound: %v", err)
	}
	c.Close()
}

// TestOwnsSocketDir distinguishes the directory sonar makes for itself from one
// the user pointed SONAR_SOCKET at.
func TestOwnsSocketDir(t *testing.T) {
	home := func() (string, error) { return "/home/dev", nil }
	env := map[string]string{"SONAR_SOCKET": "/tmp/x.sock"}
	getenv := func(k string) string { return env[k] }

	if ownsSocketDir("/tmp/x.sock", getenv, home, "linux") {
		t.Error("/tmp counted as a directory sonar owns")
	}
	if !ownsSocketDir("/home/dev/.config/sonar/daemon.sock", getenv, home, "linux") {
		t.Error("the default config directory did not count as ours")
	}

	env["XDG_RUNTIME_DIR"] = "/run/user/1000"
	if !ownsSocketDir("/run/user/1000/sonar/daemon.sock", getenv, home, "linux") {
		t.Error("the XDG runtime subdirectory did not count as ours")
	}
	if ownsSocketDir("/run/user/1000/daemon.sock", getenv, home, "linux") {
		t.Error("the XDG runtime directory itself counted as ours")
	}
}
