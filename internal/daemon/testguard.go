package daemon

import (
	"os"
	"path/filepath"
	"strings"
)

// A Go test binary must never become a sonar daemon.
//
// `go test` links each package into an executable named <pkg>.test. Handing
// that executable the arguments `serve --detach` does not start a daemon: Go's
// flag package stops parsing at the first non-flag argument, so `serve` and
// `--detach` are ignored and the binary runs the whole test suite again, in
// the background, against whatever HOME it inherited. A test that reaches the
// autostart path therefore forks a copy of itself every time it runs, and any
// test in that copy which reaches the same path forks again — which is how one
// afternoon of `go test` left 2,150 `cmd.test serve --detach` processes behind
// and exhausted swap (step 1A.20).
//
// The guard lives here because both ends need it: the client that would spawn
// the daemon (internal/daemon/client.Autostart) and the `sonar serve` that
// would become one (internal/cmd).

// AllowTestDaemonEnv opts a test binary back in to acting as a daemon. It
// exists so the guard can be exercised, and so a future harness that genuinely
// wants a test binary as its daemon can say so out loud. Nothing in the tree
// sets it.
const AllowTestDaemonEnv = "SONAR_ALLOW_TEST_DAEMON"

// IsTestBinary reports whether path names a Go test binary. `go test` always
// names one <pkg>.test (<pkg>.test.exe on Windows), whether it runs it from
// the build cache or `go test -c` writes it out.
func IsTestBinary(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	return strings.HasSuffix(base, ".test")
}

// TestDaemonAllowed reports whether AllowTestDaemonEnv opts this process in.
func TestDaemonAllowed() bool { return EnvEnabled(os.Getenv(AllowTestDaemonEnv)) }

// EnvEnabled reads a boolean environment variable the way the shell reads one:
// unset, empty, "0", "false" and "no" are off, anything else is on. Sonar's
// env switches are all opt-in flags, so "set to anything" has to mean on.
func EnvEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no":
		return false
	}
	return true
}
