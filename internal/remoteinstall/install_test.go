package remoteinstall

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/daemon"
	"github.com/raskrebs/sonar/internal/daemon/rpc"
)

// Linking this package in is what makes the method exist, the way
// internal/daemon/groupstart does for groups.start (contract §8).
func TestLinkingThisPackageInRegistersTheMethod(t *testing.T) {
	if !slices.Contains(daemon.RegisteredMethods(), "remote.install") {
		t.Fatalf("remote.install is not registered; got %v", daemon.RegisteredMethods())
	}
	if _, ok := rpc.Methods()["remote.install"]; !ok {
		t.Error("remote.install has no wire description, so it is missing from the schema")
	}
	d := rpc.Methods()["remote.install"]
	if d.Chunk == nil || d.End == nil {
		t.Error("remote.install must be described as a streaming method")
	}
}

// The destination goes to ssh verbatim and after "--", so a Host alias from
// ~/.ssh/config works and a destination that starts with a dash can never be
// read as a flag.
func TestSSHArgsPassTheDestinationThroughUntouched(t *testing.T) {
	cases := []struct {
		name string
		r    runner
		want []string
	}{
		{
			name: "plain",
			r:    runner{target: "deploy@203.0.113.7"},
			want: []string{"--", "deploy@203.0.113.7", "uname -s -m"},
		},
		{
			name: "batch mode for a daemon with no terminal",
			r:    runner{target: "box", batch: true},
			want: []string{"-o", "BatchMode=yes", "--", "box", "uname -s -m"},
		},
		{
			name: "identity and extra flags",
			r:    runner{target: "box", identity: "/keys/id_ed25519", extra: []string{"-J", "bastion"}},
			want: []string{"-i", "/keys/id_ed25519", "-J", "bastion", "--", "box", "uname -s -m"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.r.args("uname -s -m")
			if !slices.Equal(got, c.want) {
				t.Errorf("args = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseMarkerSplitsProgressLines(t *testing.T) {
	step, detail := parseMarker("sonar-install\tdownload\thttps://example.test/a.tar.gz")
	if step != "download" || detail != "https://example.test/a.tar.gz" {
		t.Errorf("parseMarker = %q, %q", step, detail)
	}
	// A tagless line is the remote's own output and is still worth showing.
	step, detail = parseMarker("sonar daemon started (pid 42)")
	if step != StepRemote || detail != "sonar daemon started (pid 42)" {
		t.Errorf("parseMarker on untagged output = %q, %q", step, detail)
	}
	// A marker with no detail is a bare step, not a crash.
	if step, detail = parseMarker("sonar-install\tservice"); step != "service" || detail != "" {
		t.Errorf("parseMarker on a bare step = %q, %q", step, detail)
	}
}

func TestFirstErrorLinePrefersTheScriptsOwnMessage(t *testing.T) {
	stderr := "Warning: Permanently added 'box' to the list of known hosts.\n" +
		ErrorMarker + "\tsonar_linux_amd64.tar.gz does not match the sha256 the release publishes\n"
	if got := firstErrorLine(stderr); got != "sonar_linux_amd64.tar.gz does not match the sha256 the release publishes" {
		t.Errorf("firstErrorLine = %q", got)
	}
	// With no marker, ssh's own last word is the useful one.
	if got := firstErrorLine("ssh: connect to host box port 22: Connection refused\n"); got != "ssh: connect to host box port 22: Connection refused" {
		t.Errorf("firstErrorLine on ssh output = %q", got)
	}
	if got := firstErrorLine("   \n\n"); got != "" {
		t.Errorf("firstErrorLine on empty stderr = %q", got)
	}
}

func TestParseVersionLineReadsTheRemoteVersion(t *testing.T) {
	out := "sonar v0.6.0 (linux/amd64)\n{\"running\": true, \"pid\": 4242}\n"
	if got := parseVersionLine(out); got != "v0.6.0" {
		t.Errorf("parseVersionLine = %q, want v0.6.0", got)
	}
	if got := parseVersionLine("sh: 1: sonar: not found\n"); got != "" {
		t.Errorf("parseVersionLine on a failed run = %q, want empty", got)
	}
}

func TestParseDaemonStatusReadsTheJSONAfterTheVersion(t *testing.T) {
	out := "sonar v0.6.0 (linux/amd64)\n{\n  \"running\": true,\n  \"pid\": 4242\n}\n"
	got, ok := parseDaemonStatus(out)
	if !ok || !got.Running || got.PID != 4242 {
		t.Errorf("parseDaemonStatus = %+v, ok=%v", got, ok)
	}
	// --no-service leaves the daemon down; that is an answer, not a failure.
	got, ok = parseDaemonStatus("sonar v0.6.0 (linux/amd64)\n{\"running\": false}\n")
	if !ok || got.Running {
		t.Errorf("parseDaemonStatus for a stopped daemon = %+v, ok=%v", got, ok)
	}
	if _, ok := parseDaemonStatus("sonar v0.6.0 (linux/amd64)\n"); ok {
		t.Error("parseDaemonStatus found a status where there was none")
	}
}

// Host names are [a-z0-9-]+ (spec 3, "CLI"), so the derived one has to be too.
func TestHostNameIsDerivedFromTheTarget(t *testing.T) {
	for target, want := range map[string]string{
		"deploy@box.example.com":      "box",
		"root@203.0.113.7":            "203-0-113-7",
		"hetzner":                     "hetzner",
		"deploy@BOX":                  "box",
		"deploy@box.example.com:2222": "box",
		"deploy@_":                    "remote",
	} {
		if got := hostName("", target); got != want {
			t.Errorf("hostName(\"\", %q) = %q, want %q", target, got, want)
		}
	}
	if got := hostName("Prod Box", "deploy@box"); got != "prod-box" {
		t.Errorf("hostName sanitises the given name to %q", got)
	}
}

func TestInstallRequiresATarget(t *testing.T) {
	if _, err := Install(context.Background(), Options{Version: "v0.6.0"}, nil); err == nil {
		t.Fatal("Install with no target returned no error")
	}
}

// The desktop branches on the code, so a host that cannot run sonar and an ssh
// that could not connect must not arrive as the same error.
func TestInstallErrorGivesActionableFailuresTheirOwnCode(t *testing.T) {
	unsupported := installError(&UnsupportedPlatformError{Kernel: "FreeBSD", Machine: "amd64", Reason: "nope"})
	var re *rpc.Error
	if !errors.As(unsupported, &re) || re.Code != rpc.CodeUnsupported {
		t.Errorf("an unsupported platform became %v", unsupported)
	}

	dev := installError(ErrDevBuild)
	if !errors.As(dev, &re) || re.Code != rpc.CodeInvalidParams {
		t.Errorf("a dev build became %v", dev)
	}
	if !strings.Contains(re.Data.Hint, "--version") {
		t.Errorf("the dev-build hint should say to pass --version, got %q", re.Data.Hint)
	}

	other := installError(errors.New("ssh box: Connection refused"))
	if !errors.As(other, &re) || re.Code != rpc.CodeInternal {
		t.Errorf("an ssh failure became %v", other)
	}

	// An error that is already an rpc.Error keeps its own code.
	already := rpc.NewError(rpc.CodeTimeout, "timed out", "")
	if got := installError(already); got != error(already) {
		t.Errorf("installError rewrapped an rpc.Error: %v", got)
	}
}

func TestEndCarriesEverythingTheCallerNeedsToRegisterTheHost(t *testing.T) {
	end := End(&Result{
		Name: "box", Target: "deploy@box", Version: "v0.6.0",
		OS: "linux", Arch: "amd64", BinPath: BinDisplay,
		Service: ServiceSystemd, DaemonRunning: true, DaemonPID: 42,
		LingerHint: "sudo loginctl enable-linger deploy",
	})
	if end.Name != "box" || end.Target != "deploy@box" || end.Version != "v0.6.0" {
		t.Errorf("end = %+v", end)
	}
	if end.Service != ServiceSystemd || !end.DaemonRunning || end.DaemonPID != 42 {
		t.Errorf("end = %+v", end)
	}
	if end.LingerHint == "" {
		t.Error("the linger hint must survive into the end payload")
	}
}
