//go:build integration

// Integration tests for `sonar remote install`, run with
// `go test -tags integration ./internal/remoteinstall/...`.
//
// There is no second machine, so the far end is faked at the two places sonar
// talks to it: a `ssh` on PATH that runs the command it is given locally, in a
// temp HOME with its own socket, and a `curl` on PATH that serves a locally
// built release archive instead of reaching GitHub. Everything between them —
// the platform detection, the generated script, the checksum verification, the
// binary landing in ~/.local/bin, the daemon starting and answering
// `daemon status` — is the real thing.
package remoteinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raskrebs/sonar/internal/testenv"
)

// testVersion is the release the fake host installs. It is injected into the
// binary the test builds, so the post-install version check compares two real
// values rather than being told what it wants to hear.
const testVersion = "v9.9.9"

func TestInstallPutsSonarOnTheHostAndStartsItsDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote script is POSIX sh; a Windows host is out of scope for milestone 3")
	}
	h := newFakeHost(t)

	var steps []string
	res, err := Install(context.Background(), Options{
		Target:   "deploy@fake",
		Resolver: h.resolver,
		Version:  testVersion,
	}, func(step, detail string) {
		steps = append(steps, step)
		t.Logf("%-9s %s", step, detail)
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Every step the caller is promised, in order.
	for _, want := range []string{
		StepConnect, StepDetect, StepResolve,
		StepDownload, StepVerify, StepExtract, StepInstall, StepService,
		StepCheck, StepDone,
	} {
		if !containsStep(steps, want) {
			t.Errorf("the install never reported step %q; got %v", want, steps)
		}
	}

	if res.Version != testVersion || res.RemoteVersion != testVersion {
		t.Errorf("version = %s, remote reports %s, want %s", res.Version, res.RemoteVersion, testVersion)
	}
	if res.OS != runtime.GOOS || res.Arch != runtime.GOARCH {
		t.Errorf("platform = %s/%s, want %s/%s", res.OS, res.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if res.Name != "fake" {
		t.Errorf("host name = %q, want fake", res.Name)
	}
	// No systemd user session behind the fake ssh, so the fallback runs.
	if res.Service != ServiceDetached {
		t.Errorf("service = %q, want %q", res.Service, ServiceDetached)
	}
	if !res.DaemonRunning || res.DaemonPID == 0 {
		t.Errorf("the daemon is not running after the install: %+v", res)
	}

	installed := filepath.Join(h.home, ".local", "bin", "sonar")
	if info, err := os.Stat(installed); err != nil {
		t.Fatalf("%s: %v", installed, err)
	} else if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is not executable (%s)", installed, info.Mode())
	}

	// Re-running is an upgrade: it must not fail on the binary or the daemon
	// already being there, and the daemon must come back.
	before := res.DaemonPID
	again, err := Install(context.Background(), Options{
		Target: "deploy@fake", Resolver: h.resolver, Version: testVersion,
	}, nil)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if !again.DaemonRunning {
		t.Error("the daemon is not running after a re-install")
	}
	if again.DaemonPID == before {
		t.Errorf("the daemon was not restarted: still pid %d", before)
	}
}

// --no-service leaves a usable binary and no daemon, and says so rather than
// failing the post-install check.
func TestInstallWithNoServiceLeavesTheDaemonStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote script is POSIX sh")
	}
	h := newFakeHost(t)

	res, err := Install(context.Background(), Options{
		Target: "deploy@fake", Resolver: h.resolver, Version: testVersion, NoService: true,
	}, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Service != ServiceNone {
		t.Errorf("service = %q, want %q", res.Service, ServiceNone)
	}
	if res.DaemonRunning {
		t.Error("--no-service started a daemon anyway")
	}
	if _, err := os.Stat(filepath.Join(h.home, ".local", "bin", "sonar")); err != nil {
		t.Errorf("--no-service did not install the binary: %v", err)
	}
}

// A corrupted download must never be installed. The fake curl serves an archive
// whose bytes do not match the checksum the resolver hands the script.
func TestInstallRefusesAnArchiveThatDoesNotMatchItsChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote script is POSIX sh")
	}
	h := newFakeHost(t)
	h.corruptArchive(t)

	_, err := Install(context.Background(), Options{
		Target: "deploy@fake", Resolver: h.resolver, Version: testVersion,
	}, nil)
	if err == nil {
		t.Fatal("Install accepted an archive that does not match its sha256")
	}
	if !strings.Contains(err.Error(), "does not match the sha256") {
		t.Errorf("error = %q, want it to name the checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.home, ".local", "bin", "sonar")); statErr == nil {
		t.Error("a binary was installed despite the checksum mismatch")
	}
}

// The systemd branch cannot run against the real user manager, so it runs
// against a stub systemctl: the point is that the unit lands where a user unit
// belongs and that the three commands an upgrade needs are the ones issued.
func TestScriptWritesAndEnablesASystemdUserUnit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the remote script is POSIX sh")
	}
	h := newFakeHost(t)
	h.stubSystemd(t)

	calls := filepath.Join(h.home, "systemctl.calls")
	out, err := h.runScript(t, Script(scriptParams{
		Version:  testVersion,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Asset:    h.asset,
		URL:      "https://example.test/" + h.asset,
		SHA256:   h.sha256,
		Service:  true,
	}))
	if err != nil {
		t.Fatalf("running the install script: %v\n%s", err, out)
	}

	unit, readErr := os.ReadFile(filepath.Join(h.home, ".config", "systemd", "user", "sonar.service"))
	if readErr != nil {
		t.Fatalf("the unit was not written: %v", readErr)
	}
	if string(unit) != Unit {
		t.Errorf("the unit on disk differs from Unit:\n%s", unit)
	}

	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("reading the systemctl calls: %v", err)
	}
	for _, want := range []string{
		"--user daemon-reload",
		"--user enable sonar.service",
		"--user restart sonar.service",
	} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("systemctl was never called with %q; got:\n%s", want, logged)
		}
	}

	if !strings.Contains(out, Marker+"\tservice\tsystemd") {
		t.Errorf("the script did not report the systemd service mode:\n%s", out)
	}
	// The stub loginctl reports Linger=no, so the advice has to appear.
	if !strings.Contains(out, Marker+"\tlinger\tsudo loginctl enable-linger") {
		t.Errorf("the script did not print the enable-linger advice:\n%s", out)
	}
}

// ---------------------------------------------------------------------------

// fakeHost is the far end: a HOME, a release directory, and the fake ssh and
// curl that stand in for the network.
type fakeHost struct {
	home    string
	binDir  string
	release string
	socket  string
	asset   string
	sha256  string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()

	sonar, err := buildSonar()
	if err != nil {
		t.Fatal(err)
	}

	h := &fakeHost{
		home:    t.TempDir(),
		binDir:  t.TempDir(),
		release: t.TempDir(),
		asset:   fmt.Sprintf("sonar_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH),
	}

	// macOS caps a unix socket path at ~104 bytes and t.TempDir() is long.
	sockDir, err := os.MkdirTemp("", "snr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	h.socket = filepath.Join(sockDir, "d.sock")

	// The release the fake curl serves, plus the checksum file the resolver
	// reads — the same two files a real release publishes.
	archive := filepath.Join(h.release, h.asset)
	writeArchive(t, archive, sonar)
	h.sha256 = fileSHA256(t, archive)
	writeFile(t, filepath.Join(h.release, "sonar_checksums.txt"),
		fmt.Sprintf("%s  %s\n", h.sha256, h.asset))

	h.writeFakeCurl(t)
	h.writeFakeSSH(t)

	// The install shells out to `ssh` by name, so the fake has to be the one
	// PATH finds. t.Setenv also stops these tests running in parallel, which
	// is what we want: they share the machine's PATH.
	t.Setenv("PATH", h.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Cleanup(func() {
		// Whatever the test did, do not leave a daemon behind.
		cmd := exec.Command(filepath.Join(h.home, ".local", "bin", "sonar"), "daemon", "stop")
		cmd.Env = h.remoteEnv()
		_ = cmd.Run()
	})
	return h
}

// remoteEnv is what the fake ssh exports before running a command: the far
// end's HOME, its own socket and database, and a PATH whose first entry holds
// the fakes.
func (h *fakeHost) remoteEnv() []string {
	return []string{
		"HOME=" + h.home,
		"PATH=" + h.binDir + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"SONAR_SOCKET=" + h.socket,
		"SONAR_DB=" + filepath.Join(h.home, "sonar.db"),
		"NO_COLOR=1",
		"TMPDIR=" + h.home,
	}
}

// resolver stands in for GitHub: it points at a URL the fake curl serves and
// returns the sha256 from the release's own checksum file.
func (h *fakeHost) resolver(_ context.Context, version, asset string) (string, string, error) {
	if version != testVersion {
		return "", "", fmt.Errorf("no release %s", version)
	}
	body, err := os.ReadFile(filepath.Join(h.release, "sonar_checksums.txt"))
	if err != nil {
		return "", "", err
	}
	sum := ParseChecksums(string(body))[asset]
	if sum == "" {
		return "", "", fmt.Errorf("no checksum for %s", asset)
	}
	return "https://example.test/" + asset, sum, nil
}

// corruptArchive rewrites the served archive without touching the checksum
// file, which is what a truncated download or a tampered mirror looks like.
func (h *fakeHost) corruptArchive(t *testing.T) {
	t.Helper()
	writeFile(t, filepath.Join(h.release, h.asset), "not a tarball at all\n")
}

// writeFakeSSH puts an `ssh` on PATH that runs the command locally. It skips
// ssh's flags up to the "--" every invocation uses, drops the destination, and
// hands the single remaining argument to sh — which is exactly what a real ssh
// does with it, one machine over.
func (h *fakeHost) writeFakeSSH(t *testing.T) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"export HOME=" + shQuote(h.home) + "\n" +
		"export PATH=" + shQuote(h.binDir+":/usr/bin:/bin:/usr/sbin:/sbin") + "\n" +
		"export SONAR_SOCKET=" + shQuote(h.socket) + "\n" +
		"export SONAR_DB=" + shQuote(filepath.Join(h.home, "sonar.db")) + "\n" +
		"export TMPDIR=" + shQuote(h.home) + "\n" +
		"export NO_COLOR=1\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--\" ]; then shift; break; fi\n" +
		"  shift\n" +
		"done\n" +
		"shift\n" + // the destination
		"exec sh -c \"$1\"\n"
	writeExecutable(t, filepath.Join(h.binDir, "ssh"), script)
}

// writeFakeCurl serves the release directory by the URL's last path element,
// and 404s like curl -f on anything else.
func (h *fakeHost) writeFakeCurl(t *testing.T) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"out=\n" +
		"url=\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -o) out=$2; shift 2;;\n" +
		"    --) shift;;\n" +
		"    -*) shift;;\n" +
		"    *) url=$1; shift;;\n" +
		"  esac\n" +
		"done\n" +
		"src=" + shQuote(h.release) + "/$(basename \"$url\")\n" +
		"[ -f \"$src\" ] || { echo \"curl: (22) 404 for $url\" >&2; exit 22; }\n" +
		"cp \"$src\" \"$out\"\n"
	writeExecutable(t, filepath.Join(h.binDir, "curl"), script)
	// wget must not be picked up from the machine running the test.
	writeExecutable(t, filepath.Join(h.binDir, "wget"), "#!/bin/sh\nexit 127\n")
}

// stubSystemd replaces systemctl and loginctl with stubs that log their
// arguments, so the systemd branch of the script can run without touching the
// user manager of the machine running the test.
func (h *fakeHost) stubSystemd(t *testing.T) {
	t.Helper()
	calls := filepath.Join(h.home, "systemctl.calls")
	writeExecutable(t, filepath.Join(h.binDir, "systemctl"),
		"#!/bin/sh\necho \"$@\" >>"+shQuote(calls)+"\nexit 0\n")
	writeExecutable(t, filepath.Join(h.binDir, "loginctl"),
		"#!/bin/sh\ncase \"$*\" in *Linger*) echo no;; esac\nexit 0\n")
}

// runScript runs a generated install script the way ssh would, and returns its
// combined output.
func (h *fakeHost) runScript(t *testing.T, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = h.remoteEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// buildSonar compiles the CLI once per test run, with the release version this
// test pretends to be installing baked in.
// The binary goes under testenv.Root(): the leak gate only claims a `serve`
// whose executable lives inside this run's own temp root, so a daemon this
// harness starts has to be built there to be recognised — and one another run
// built has to be somewhere else to be left alone.
var buildSonar = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp(testenv.Root(), "sonar-ri-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "sonar")
	cmd := exec.Command("go", "build",
		"-ldflags=-X github.com/raskrebs/sonar/internal/selfupdate.Version="+testVersion,
		"-o", bin, ".")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building sonar: %v: %s", err, out)
	}
	return bin, nil
})

func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "."
}

// writeArchive packs one binary as sonar_<os>_<arch>.tar.gz does: a single
// entry named "sonar" at the root.
func writeArchive(t *testing.T, path, binary string) {
	t.Helper()
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "sonar", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// shQuote is single-quoting for a path that goes into a generated shell script.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func containsStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}
