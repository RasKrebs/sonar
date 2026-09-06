// Package testenv isolates a Go test binary from the machine it runs on.
//
// Every path sonar resolves from the environment — the config directory, the
// database, the socket, the daemon log, the run logs — hangs off HOME, so a
// test that reaches any of them reaches the developer's live install. Two
// things went wrong at once before step 1A.20: in-process test daemons opened
// the real ~/.config/sonar/sonar.db, and a test that touched the CLI's
// autostart path forked `<pkg>.test serve --detach`, which is not a daemon but
// a background re-run of the whole suite (see daemon.IsTestBinary). One
// afternoon of `go test` left 2,150 of those behind and exhausted swap.
//
// The fix is isolation by construction rather than per test: every package
// whose tests can reach the daemon or the config directory calls Run from its
// TestMain, and from then on nothing in that binary can find the real install
// even if it tries.
//
// A run also gets a private root under the machine's temp directory, which
// becomes its TMPDIR (Root). t.TempDir, os.CreateTemp and a harness that
// builds the real binary with `go build -o` therefore all write inside it, and
// the leak gate can name the daemons this run is responsible for without
// touching another `go test` running beside it on the same machine.
//
// Usage:
//
//	func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }
//
// A TestMain that also serves as a helper-process entry point isolates itself
// and keeps control:
//
//	func TestMain(m *testing.M) {
//		if os.Getenv(envHelper) != "" { runHelper(); return }
//		os.Exit(testenv.Run(m))
//	}
//
// This package deliberately imports nothing from the module: it is linked into
// every test binary that uses it, and a helper that dragged in the daemon and
// SQLite would make a display-only test binary far heavier than its subject.
// The check that the *real* resolvers agree lives in the packages that already
// import them, via Isolated.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Environment variables Isolate sets. They are the complete set sonar honours
// for locating anything; adding a new one means adding it here too.
const (
	noAutostartEnv = "SONAR_NO_AUTOSTART"
	socketEnv      = "SONAR_SOCKET"
	dbEnv          = "SONAR_DB"
	allowTestEnv   = "SONAR_ALLOW_TEST_DAEMON"
	gateAllEnv     = "SONAR_TESTENV_GATE_ALL"
)

// runRoot is this run's private directory under the machine's temp directory
// and the TMPDIR every child inherits; home is the isolated HOME inside it,
// and sockRoot the short directory holding the socket. realTemp is the
// machine's temp directory as it was before runRoot displaced it. They are set
// once, by Isolate, before any test runs.
var (
	runRoot  string
	home     string
	sockRoot string
	realHome string
	realTemp string
)

// Run isolates the process, runs the tests, and fails the run if the suite
// left a daemon behind. It returns the exit code TestMain should pass to
// os.Exit.
func Run(m *testing.M) int {
	cleanup := Isolate()
	defer cleanup()

	code := m.Run()

	if leaked := AssertNoLeakedDaemons(); len(leaked) > 0 && code == 0 {
		code = 1
	}
	return code
}

// Isolate points every path sonar resolves from the environment at a private
// temp directory, and switches daemon autostart off. It returns a cleanup that
// removes the directories.
//
// It exits the process rather than returning an error when it cannot make the
// environment safe: a test binary that keeps going here writes to the
// developer's real install, and there is no test result worth that.
func Isolate() func() {
	realHome, _ = os.UserHomeDir()
	realTemp = os.TempDir()
	preserveGoEnv()

	// One root per run, and TMPDIR points at it, so t.TempDir, os.CreateTemp and
	// a harness `go build -o` all land in a directory this run owns. That
	// ownership is what lets the leak gate tell a daemon this run started from
	// one another `go test` on the same machine is still using: every run
	// shares os.TempDir(), and a gate that matched all of it killed its
	// neighbours.
	//
	// The names are terse because they are a prefix of every socket path, and
	// macOS caps a unix socket at about 104 bytes.
	run, err := os.MkdirTemp("", "snrun")
	if err != nil {
		die("creating this run's temp root: %v", err)
	}
	runRoot = run
	setTempDir(run)

	// The socket directory is a sibling of HOME rather than a child: macOS caps
	// a unix socket path at about 104 bytes and
	// "<run>/h/.config/sonar/daemon.sock" leaves too little room.
	home, sockRoot = filepath.Join(run, "h"), filepath.Join(run, "s")
	if err := os.MkdirAll(sockRoot, 0o700); err != nil {
		die("creating an isolated socket directory: %v", err)
	}

	configDir := filepath.Join(home, ".config", "sonar")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		die("creating %s: %v", configDir, err)
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		die("creating the isolated XDG_RUNTIME_DIR: %v", err)
	}

	set("HOME", home)                                      // unix
	set("USERPROFILE", home)                               // windows
	set("XDG_CONFIG_HOME", filepath.Join(home, ".config")) // freedesktop
	set("XDG_RUNTIME_DIR", filepath.Join(home, "run"))     // outranks HOME for the socket
	set(dbEnv, filepath.Join(configDir, "sonar.db"))
	set(socketEnv, socketPath(sockRoot))
	set(noAutostartEnv, "1")
	// A leftover opt-in from the developer's shell must not disarm the guard
	// that keeps a test binary from becoming a daemon.
	if err := os.Unsetenv(allowTestEnv); err != nil {
		die("unsetting %s: %v", allowTestEnv, err)
	}

	// Belt and braces: prove the environment now resolves somewhere else. A
	// sandbox that silently ignored Setenv would otherwise leave every test
	// pointing at the live install.
	if realHome != "" && sameDir(os.Getenv("HOME"), realHome) {
		die("HOME is still %s; refusing to run tests against the live install", realHome)
	}
	if !Isolated(configDir) {
		die("the isolated config directory %s is not under %s", configDir, runRoot)
	}
	if !sameDir(os.TempDir(), runRoot) {
		die("os.TempDir() is %s, not this run's root %s", os.TempDir(), runRoot)
	}

	return func() { os.RemoveAll(runRoot) }
}

// setTempDir makes dir the temp directory of this process and of every child
// it spawns, so that everything a test writes outside its own HOME still lands
// in a directory this run owns.
func setTempDir(dir string) {
	set("TMPDIR", dir) // unix, and what os.TempDir reads there
	if os.PathSeparator == '\\' {
		set("TMP", dir) // windows, in the order os.TempDir consults them
		set("TEMP", dir)
	}
}

// preserveGoEnv pins the Go toolchain's caches to where they already are.
//
// Go derives GOPATH, GOCACHE, GOMODCACHE and GOENV from the home directory,
// and several integration harnesses run `go build` to produce the real `sonar`
// binary they drive. Moving HOME without this points those builds at an empty
// module cache inside a temp directory that is deleted at the end of the run:
// they re-download the whole module graph, and offline they simply fail.
// Resolving the defaults here, while HOME is still the real one, is exactly
// what the toolchain would have done. A variable already set in the
// environment is left alone — it does not depend on HOME in the first place.
func preserveGoEnv() {
	if cache, err := os.UserCacheDir(); err == nil {
		setDefault("GOCACHE", filepath.Join(cache, "go-build"))
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		setDefault("GOENV", filepath.Join(cfg, "go", "env"))
	}
	if realHome != "" {
		setDefault("GOPATH", filepath.Join(realHome, "go"))
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		setDefault("GOMODCACHE", filepath.Join(gopath, "pkg", "mod"))
	}
}

func setDefault(key, value string) {
	if os.Getenv(key) == "" && value != "" {
		set(key, value)
	}
}

// Root is this run's private temp root: the directory TMPDIR points at while
// the tests run, and the one a harness building a binary should build into so
// the leak gate recognises what it starts. It is empty before Isolate runs.
func Root() string { return runRoot }

// Home is the isolated HOME, inside Root.
func Home() string { return home }

// RealHome is the home directory this run displaced: the one the developer's
// live sonar install lives in. Isolate is the only chance to read it, because
// afterwards os.UserHomeDir answers with Root.
func RealHome() string { return realHome }

// ConfigDir is the isolated ~/.config/sonar.
func ConfigDir() string { return filepath.Join(home, ".config", "sonar") }

// Isolated reports whether path lies inside this run's own temp root. It is
// what a package's own TestMain uses to check the real resolvers agree:
//
//	if !testenv.Isolated(config.Path(), store.Path(), daemon.SocketPath()) { ... }
func Isolated(paths ...string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if p == "" || !under(p, runRoot) {
			return false
		}
	}
	return true
}

// RequireIsolated fails the test unless every path is inside this run's own
// temp root.
func RequireIsolated(t testing.TB, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if !Isolated(p) {
			t.Fatalf("%s is outside the isolated test environment (HOME=%s, TMPDIR=%s)",
				p, home, runRoot)
		}
	}
}

// AllowAutostart lets one test use the real autostart path, and restores the
// ban afterwards. Only a test that owns the daemon it starts and stops it in a
// cleanup may call it.
func AllowAutostart(t testing.TB) {
	t.Helper()
	t.Setenv(noAutostartEnv, "")
}

// ChildEnv is what a harness appends to a child process's environment when
// that child is the real `sonar` binary and is allowed to autostart a daemon
// of its own. The child is not a test binary, so only the process-wide ban has
// to be lifted.
func ChildEnv() string { return noAutostartEnv + "=" }

func socketPath(dir string) string {
	// Named pipes share one flat namespace, so a Windows test binary takes an
	// address stamped with its pid rather than a path.
	if os.PathSeparator == '\\' {
		return fmt.Sprintf(`\\.\pipe\sonar-test-%d`, os.Getpid())
	}
	return filepath.Join(dir, "d.sock")
}

func set(key, value string) {
	if err := os.Setenv(key, value); err != nil {
		die("setting %s: %v", key, err)
	}
}

func under(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sameDir(a, b string) bool { return resolve(a) == resolve(b) }

// resolve cleans a path and follows symlinks where it can. /var on macOS is a
// symlink to /private/var, so os.TempDir() and a path built from HOME can name
// the same directory in two spellings. A path that does not exist yet — a
// database file a test has not created — still has to resolve, so the walk
// stops at the deepest ancestor that does and keeps the rest verbatim.
func resolve(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for dir := path; ; {
		if p, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(p, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "testenv: "+format+"\n", args...)
	os.Exit(1)
}
