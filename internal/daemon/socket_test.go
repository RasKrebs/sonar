package daemon

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestSocketPathResolution walks contract §7's resolution order across the
// whole environment matrix, on one host, for both platform branches.
func TestSocketPathResolution(t *testing.T) {
	home := func() (string, error) { return "/home/dev", nil }

	tests := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{
			name: "SONAR_SOCKET wins over everything",
			goos: "darwin",
			env:  map[string]string{"SONAR_SOCKET": "/tmp/x.sock", "XDG_RUNTIME_DIR": "/run/user/1000"},
			want: "/tmp/x.sock",
		},
		{
			name: "SONAR_SOCKET wins on Windows too",
			goos: "windows",
			env:  map[string]string{"SONAR_SOCKET": `\\.\pipe\sonar-test`},
			want: `\\.\pipe\sonar-test`,
		},
		{
			name: "empty SONAR_SOCKET is not set",
			goos: "linux",
			env:  map[string]string{"SONAR_SOCKET": "", "XDG_RUNTIME_DIR": "/run/user/1000"},
			want: filepath.Join("/run/user/1000", "sonar", "daemon.sock"),
		},
		{
			name: "Windows uses the named pipe",
			goos: "windows",
			env:  map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			want: WindowsPipe,
		},
		{
			name: "XDG_RUNTIME_DIR when set",
			goos: "linux",
			env:  map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			want: filepath.Join("/run/user/1000", "sonar", "daemon.sock"),
		},
		{
			name: "home config directory otherwise",
			goos: "darwin",
			env:  map[string]string{},
			want: filepath.Join("/home/dev", ".config", "sonar", "daemon.sock"),
		},
		{
			name: "empty XDG_RUNTIME_DIR falls through to home",
			goos: "linux",
			env:  map[string]string{"XDG_RUNTIME_DIR": ""},
			want: filepath.Join("/home/dev", ".config", "sonar", "daemon.sock"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			if got := socketPathFrom(getenv, home, tt.goos); got != tt.want {
				t.Errorf("socketPathFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSocketPathHomeFailure covers the unhappy path where HOME is unset: the
// daemon must not fall back to the filesystem root.
func TestSocketPathHomeFailure(t *testing.T) {
	getenv := func(string) string { return "" }
	home := func() (string, error) { return "", errNoHome{} }
	want := filepath.Join(".", ".config", "sonar", "daemon.sock")
	if got := socketPathFrom(getenv, home, "linux"); got != want {
		t.Errorf("socketPathFrom() = %q, want %q", got, want)
	}
}

type errNoHome struct{}

func (errNoHome) Error() string { return "no home" }

// TestLockPathFollowsSocket checks that the lock lives beside the socket, so a
// socket on a per-boot tmpfs takes its lock with it, and that a named pipe
// (which has no directory) falls back to the config directory.
func TestLockPathFollowsSocket(t *testing.T) {
	// The "beside the socket" branch is a unix-socket branch: on Windows the
	// address is always a named pipe (the default, or a SONAR_SOCKET pointing
	// at another pipe), which has no directory and takes the config-dir branch
	// below. Asserting a unix path there would only assert what filepath does
	// to slashes.
	if runtime.GOOS != "windows" {
		if got, want := lockPathFor("/run/user/1000/sonar/daemon.sock"),
			"/run/user/1000/sonar/daemon.lock"; got != want {
			t.Errorf("lockPathFor(unix socket) = %q, want %q", got, want)
		}
	}
	got := lockPathFor(WindowsPipe)
	if want := filepath.Join(ConfigDir(), "daemon.lock"); got != want {
		t.Errorf("lockPathFor(named pipe) = %q, want %q", got, want)
	}
}
