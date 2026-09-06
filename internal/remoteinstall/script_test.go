package remoteinstall

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden rewrites testdata from the generator instead of comparing
// against it: `go test ./internal/remoteinstall -update`. Review the diff.
var updateGolden = flag.Bool("update", false, "rewrite the golden scripts in testdata")

// The script is the whole of what runs on someone else's machine, as root-less
// as it is, and it is generated rather than shipped as a file. Pinning its
// exact text is the only way a change to it is a change someone reads.
func TestScriptIsExactlyTheGoldenText(t *testing.T) {
	cases := []struct {
		golden string
		params scriptParams
	}{
		{"install_linux_amd64.sh", scriptParams{
			Version:  "v0.6.0",
			Platform: "linux/amd64",
			Asset:    "sonar_linux_amd64.tar.gz",
			URL:      "https://github.com/raskrebs/sonar/releases/download/v0.6.0/sonar_linux_amd64.tar.gz",
			SHA256:   "f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909",
			Service:  true,
		}},
		{"install_linux_arm64.sh", scriptParams{
			Version:  "v0.6.0",
			Platform: "linux/arm64",
			Asset:    "sonar_linux_arm64.tar.gz",
			URL:      "https://github.com/raskrebs/sonar/releases/download/v0.6.0/sonar_linux_arm64.tar.gz",
			SHA256:   "e48570a81434686696dcef983f4758c8c3a7ed2aad4e4f159106257b51aed365",
			Service:  true,
		}},
		{"install_no_service.sh", scriptParams{
			Version:  "v0.6.0",
			Platform: "linux/amd64",
			Asset:    "sonar_linux_amd64.tar.gz",
			URL:      "https://github.com/raskrebs/sonar/releases/download/v0.6.0/sonar_linux_amd64.tar.gz",
			SHA256:   "f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909",
		}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			path := filepath.Join("testdata", c.golden)
			got := Script(c.params)
			if *updateGolden {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("generated script differs from testdata/%s\n--- got ---\n%s", c.golden, got)
			}
		})
	}
}

func TestUnitIsExactlyTheGoldenText(t *testing.T) {
	path := filepath.Join("testdata", "sonar.service")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(Unit), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if Unit != string(want) {
		t.Errorf("systemd unit differs from testdata/sonar.service\n--- got ---\n%s", Unit)
	}
}

// The unit's specifics, said out loud so that dropping one is a test failure
// and not a silent regression on someone's server.
func TestUnitIsAUserUnitThatDoesNotKillTheServicesItStarted(t *testing.T) {
	for _, line := range []string{
		"ExecStart=%h/.local/bin/sonar serve",
		// A group's services live in this unit's cgroup however detached they
		// are; the default control-group kill mode would stop them all on an
		// upgrade's restart.
		"KillMode=process",
		"Restart=on-failure",
		// default.target, not multi-user.target: this is a user unit.
		"WantedBy=default.target",
	} {
		if !strings.Contains(Unit, line) {
			t.Errorf("the systemd unit is missing %q", line)
		}
	}
	if strings.Contains(Unit, "User=") || strings.Contains(Unit, "sudo") {
		t.Error("the systemd unit is a user unit and must not name a user or need root")
	}
}

// The checksum verification is the one step that must never be skippable, so
// it gets its own assertions rather than only the golden comparison.
func TestScriptVerifiesTheDownloadBeforeInstallingIt(t *testing.T) {
	sum := "f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909"
	got := Script(scriptParams{
		Version:  "v0.6.0",
		Platform: "linux/amd64",
		Asset:    "sonar_linux_amd64.tar.gz",
		URL:      "https://example.invalid/sonar_linux_amd64.tar.gz",
		SHA256:   sum,
		Service:  true,
	})
	for _, want := range []string{
		`SHA256="` + sum + `"`,
		`verify() { sha256sum -c "$1" >/dev/null; }`,
		`verify() { shasum -a 256 -c "$1" >/dev/null; }`,
		`printf '%s  %s\n' "$SHA256" "$ASSET" >"$TMP/$ASSET.sha256"`,
		`(cd "$TMP" && verify "$ASSET.sha256") || die "$ASSET does not match the sha256 the release publishes"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the install script is missing %q", want)
		}
	}
	// The verification has to come before anything is put on the PATH.
	if strings.Index(got, "verify \"$ASSET.sha256\"") > strings.Index(got, `mv -f "$BIN.new" "$BIN"`) {
		t.Error("the script installs the binary before verifying the archive")
	}
	// set -e is what makes an unhandled failure stop the script.
	if !strings.HasPrefix(got, "#!/bin/sh\n") || !strings.Contains(got, "\nset -eu\n") {
		t.Error("the script must be POSIX sh with `set -eu`")
	}
}

// A remote login shell may be dash, ash or ksh, so the script must parse as
// plain POSIX sh and not only as bash.
func TestScriptIsValidPOSIXShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	for _, golden := range []string{"install_linux_amd64.sh", "install_linux_arm64.sh", "install_no_service.sh"} {
		path := filepath.Join("testdata", golden)
		if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", path, err, out)
		}
	}
}

func TestScriptInstallsWithoutRootAndUnderTheHomeDirectory(t *testing.T) {
	got := Script(scriptParams{Version: "v0.6.0", Platform: "linux/amd64", Service: true})
	// The only sudo in the script is inside the advice it prints about
	// `loginctl enable-linger`, which the user runs themselves.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "sudo ") && !strings.Contains(line, "say linger") {
			t.Errorf("the install script must never run sudo itself: %s", line)
		}
	}
	if !strings.Contains(got, `BIN_DIR="$HOME/.local/bin"`) {
		t.Error("the binary must land in ~/.local/bin")
	}
	if strings.Contains(got, "/usr/local/bin/sonar") {
		t.Error("the install script must not write outside the home directory")
	}
}
