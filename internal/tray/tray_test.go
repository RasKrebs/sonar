package tray

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFS turns a list of paths into the two lookups Decide makes.
func fakeFS(paths ...string) (func(string) bool, func(string) (string, error)) {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	exists := func(p string) bool { return set[p] }
	lookPath := func(name string) (string, error) {
		for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/opt/homebrew/bin"} {
			if candidate := filepath.Join(dir, name); set[candidate] {
				return candidate, nil
			}
		}
		return "", errors.New("not found: " + name)
	}
	return exists, lookPath
}

func TestDecide(t *testing.T) {
	const home = "/home/dev"
	tests := []struct {
		name     string
		goos     string
		selfDir  string
		env      map[string]string
		files    []string
		want     Kind
		wantPath string
	}{
		{
			name:  "macOS with the app installed",
			goos:  "darwin",
			files: []string{"/Applications/Sonar.app"},
			want:  KindDesktopApp, wantPath: "/Applications/Sonar.app",
		},
		{
			name:  "macOS with the app in the user's Applications",
			goos:  "darwin",
			files: []string{filepath.Join(home, "Applications", "Sonar.app")},
			want:  KindDesktopApp, wantPath: filepath.Join(home, "Applications", "Sonar.app"),
		},
		{
			name:    "macOS without the app falls back to the Swift tray beside sonar",
			goos:    "darwin",
			selfDir: "/opt/homebrew/bin",
			files:   []string{"/opt/homebrew/bin/sonar-tray"},
			want:    KindSwiftTray, wantPath: "/opt/homebrew/bin/sonar-tray",
		},
		{
			name:  "macOS falls back to a Swift tray on PATH",
			goos:  "darwin",
			files: []string{"/usr/local/bin/sonar-tray"},
			want:  KindSwiftTray, wantPath: "/usr/local/bin/sonar-tray",
		},
		{
			name:    "the app wins over the Swift tray",
			goos:    "darwin",
			selfDir: "/opt/homebrew/bin",
			files:   []string{"/Applications/Sonar.app", "/opt/homebrew/bin/sonar-tray"},
			want:    KindDesktopApp, wantPath: "/Applications/Sonar.app",
		},
		{
			name: "macOS with nothing installed",
			goos: "darwin",
			want: KindNone,
		},
		{
			name:  "linux with sonar-desktop on PATH",
			goos:  "linux",
			files: []string{"/usr/local/bin/sonar-desktop"},
			want:  KindDesktopApp, wantPath: "/usr/local/bin/sonar-desktop",
		},
		{
			name:  "linux with a packaged install",
			goos:  "linux",
			files: []string{"/opt/Sonar/sonar-desktop"},
			want:  KindDesktopApp, wantPath: "/opt/Sonar/sonar-desktop",
		},
		{
			name:  "linux never falls back to the macOS Swift tray",
			goos:  "linux",
			files: []string{"/usr/local/bin/sonar-tray"},
			want:  KindNone,
		},
		{
			name: "windows user install",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": `C:\Users\dev\AppData\Local`},
			files: []string{
				filepath.Join(`C:\Users\dev\AppData\Local`, "Programs", "Sonar", "Sonar.exe"),
			},
			want:     KindDesktopApp,
			wantPath: filepath.Join(`C:\Users\dev\AppData\Local`, "Programs", "Sonar", "Sonar.exe"),
		},
		{
			name: "windows machine-wide install",
			goos: "windows",
			env:  map[string]string{"ProgramFiles": `C:\Program Files`},
			files: []string{
				filepath.Join(`C:\Program Files`, "Sonar", "Sonar.exe"),
			},
			want:     KindDesktopApp,
			wantPath: filepath.Join(`C:\Program Files`, "Sonar", "Sonar.exe"),
		},
		{
			name: "windows with nothing installed",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": `C:\Users\dev\AppData\Local`},
			want: KindNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exists, lookPath := fakeFS(tt.files...)
			got := Decide(Env{
				GOOS:    tt.goos,
				Home:    home,
				SelfDir: tt.selfDir,
				Getenv:  func(k string) string { return tt.env[k] },
				Exists:  exists, LookPath: lookPath,
			})
			if got.Kind != tt.want {
				t.Fatalf("Decide() = %s (%s), want %s", got.Kind, got.Path, tt.want)
			}
			if tt.wantPath != "" && got.Path != tt.wantPath {
				t.Errorf("Decide() path = %q, want %q", got.Path, tt.wantPath)
			}
		})
	}
}

// A machine with nothing installed must be told where to get the app rather
// than seeing "system tray is currently only supported on macOS".
func TestRunWithoutAnythingInstalledPointsAtTheDownload(t *testing.T) {
	err := run(Decision{Kind: KindNone}, false)
	var notInstalled *NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Fatalf("run() = %v, want a NotInstalledError", err)
	}
	if got := err.Error(); !strings.Contains(got, DownloadURL) {
		t.Errorf("error %q does not carry the download URL", got)
	}
}

// The real environment must produce usable lookups; this guards against a nil
// function slipping into Env and turning every decision into "not installed".
func TestOSEnvIsWiredUp(t *testing.T) {
	env := osEnv()
	if env.Exists == nil || env.LookPath == nil || env.Getenv == nil {
		t.Fatal("osEnv() left a lookup nil")
	}
	if env.GOOS == "" {
		t.Error("osEnv() has no GOOS")
	}
	if _, err := env.LookPath("definitely-not-a-real-binary-xyz"); err == nil {
		t.Error("LookPath found a binary that does not exist")
	}
}
