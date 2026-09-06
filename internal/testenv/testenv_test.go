package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// The guarantee the whole package exists for: after TestMain, nothing this
// binary resolves from the environment points at the machine's real install.
func TestIsolateMovesEverythingIntoTempDir(t *testing.T) {
	if Root() == "" {
		t.Fatal("Isolate did not run before the tests")
	}
	RequireIsolated(t, Home(), ConfigDir(), os.Getenv("HOME"), os.Getenv("SONAR_DB"))

	real, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolving the home directory: %v", err)
	}
	if real != Home() {
		t.Fatalf("os.UserHomeDir() = %s, want the isolated %s", real, Home())
	}
	if _, err := os.Stat(ConfigDir()); err != nil {
		t.Errorf("the isolated config directory is not there: %v", err)
	}
}

// Autostart is off for the whole binary, and the opt-in a developer may have
// in their shell does not survive into a test run.
func TestIsolateDisablesAutostart(t *testing.T) {
	if got := os.Getenv(noAutostartEnv); got == "" {
		t.Errorf("%s = %q, want it set", noAutostartEnv, got)
	}
	if got, ok := os.LookupEnv(allowTestEnv); ok {
		t.Errorf("%s = %q, want it unset", allowTestEnv, got)
	}
}

// Isolation must not take the Go build cache with it: the integration
// harnesses run `go build` to make the binary they drive, and a module cache
// inside the throwaway HOME means re-downloading the module graph every run,
// and failing outright with no network.
func TestIsolateKeepsTheGoCachesWhereTheyWere(t *testing.T) {
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		v := os.Getenv(key)
		if v == "" {
			t.Errorf("%s is unset; a `go build` from a test would rebuild the world", key)
			continue
		}
		if Isolated(v) {
			t.Errorf("%s = %s, which is inside the throwaway test environment", key, v)
		}
	}
}

func TestIsolatedRejectsPathsOutside(t *testing.T) {
	if Isolated("/etc/passwd") {
		t.Error("/etc/passwd counted as isolated")
	}
	// Another run's directory is under the same machine temp directory and is
	// no more this run's business than /etc is.
	if Isolated(filepath.Join(machineTempDir(), "snrun-some-other-run")) {
		t.Error("another run's temp directory counted as isolated")
	}
	if Isolated() {
		t.Error("no paths at all counted as isolated")
	}
	if !Isolated(filepath.Join(Home(), ".config", "sonar", "sonar.db")) {
		t.Error("a path under the isolated HOME did not count as isolated")
	}
}

// The leak gate reads `ps` output, so the parsing is worth pinning: a gate
// that quietly matched nothing would be worse than no gate.
func TestIsDaemonOf(t *testing.T) {
	const exe = "/tmp/go-build123/b001/cmd.test"
	tests := []struct {
		cmdline string
		want    bool
	}{
		{exe + " serve --detach", true},
		{exe + " serve", true},
		{exe + " -test.run=TestX", false},
		{exe, false},
		// Another package's test binary is another package's problem: `go test
		// ./...` runs them at the same time.
		{"/tmp/go-build123/b002/cmd.test serve --detach", false},
		{"cmd.test serve", false},
		{"/usr/local/bin/sonar serve", false}, // the developer's own daemon
		{"grep serve", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isDaemonOf(exe, tt.cmdline); got != tt.want {
			t.Errorf("isDaemonOf(%q) = %v, want %v", tt.cmdline, got, tt.want)
		}
	}
}

// isRunRootDaemon is the default second rule: anything under this run's root,
// and nothing else, because nothing else put anything there.
func TestIsRunRootDaemon(t *testing.T) {
	inRoot := filepath.Join(Root(), "sonar-itest-bin123", "sonar")
	if !isRunRootDaemon(inRoot + " serve") {
		t.Errorf("%s serve was not recognised as this run's daemon", inRoot)
	}
	if isRunRootDaemon(inRoot + " list") {
		t.Error("`sonar list` was mistaken for a daemon")
	}
	if isRunRootDaemon("/usr/local/bin/sonar serve") {
		t.Error("an installed sonar daemon was mistaken for a test leak")
	}
	// The whole point: another run's binary is under the machine's temp
	// directory too, and must not be touched.
	other := filepath.Join(machineTempDir(), "snrun999", "sonar-itest-bin", "sonar")
	if isRunRootDaemon(other + " serve") {
		t.Errorf("%s serve, which belongs to another test run, matched", other)
	}
}

// The opt-in wide net for CI keeps the old shape: any `sonar serve` from
// anywhere under the machine's temp directory.
func TestIsTempBuiltDaemon(t *testing.T) {
	inTemp := filepath.Join(machineTempDir(), "sonar-itest-bin123", "sonar")
	if !isTempBuiltDaemon(inTemp + " serve") {
		t.Errorf("%s serve was not recognised as a test-built daemon", inTemp)
	}
	if isTempBuiltDaemon("/usr/local/bin/sonar serve") {
		t.Error("an installed sonar daemon was mistaken for a test leak")
	}
	if isTempBuiltDaemon(inTemp + " list") {
		t.Error("`sonar list` was mistaken for a daemon")
	}
}

// `ps -axo command=` joins argv with spaces and quotes nothing, so the
// executable of a `serve` has to be found by the argument boundary rather than
// by splitting on whitespace.
func TestSplitServe(t *testing.T) {
	tests := []struct {
		cmdline string
		want    string
		ok      bool
	}{
		{"/tmp/x/sonar serve", "/tmp/x/sonar", true},
		{"/tmp/x/sonar serve --detach", "/tmp/x/sonar", true},
		{"/tmp/my dir/sonar serve --detach", "/tmp/my dir/sonar", true},
		// A directory called "serve" is not the argument.
		{"/tmp/a serve/sonar serve", "/tmp/a serve/sonar", true},
		{"/tmp/x/sonar serveall", "", false},
		{"/tmp/x/sonar list", "", false},
		{"/tmp/x/sonar", "", false},
		{"serve", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := splitServe(tt.cmdline)
		if got != tt.want || ok != tt.ok {
			t.Errorf("splitServe(%q) = (%q, %v), want (%q, %v)", tt.cmdline, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSplitPS(t *testing.T) {
	pid, cmdline, ok := splitPS("  4242 /path/to/cmd.test serve --detach")
	if !ok || pid != 4242 || cmdline != "/path/to/cmd.test serve --detach" {
		t.Errorf("splitPS = (%d, %q, %v)", pid, cmdline, ok)
	}
	if _, _, ok := splitPS("not-a-row"); ok {
		t.Error("a malformed ps row parsed as a process")
	}
}

// Nothing in this package starts a daemon, so the gate has to agree.
func TestNoDaemonsLeakFromThisBinary(t *testing.T) {
	RequireNoLeakedDaemons(t)
}
