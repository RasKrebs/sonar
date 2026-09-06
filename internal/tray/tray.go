// Package tray implements `sonar tray`. The command used to be one thing —
// start the macOS Swift menu bar app — and is now a launcher: if the Sonar
// desktop app is installed it opens that, otherwise it falls back to the Swift
// tray where that binary still exists, and otherwise it tells the user where to
// get the app (daemon spec, "Migration and deprecation": `sonar tray` launches
// the desktop app; the Swift tray leaves the release tarball once the app
// ships).
package tray

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DownloadURL is where the desktop app is published. It is a placeholder until
// the app has a release page of its own.
const DownloadURL = "https://github.com/raskrebs/sonar/releases"

// Kind is what `sonar tray` decided to launch.
type Kind int

const (
	// KindNone means nothing is installed and the user gets the download hint.
	KindNone Kind = iota
	// KindDesktopApp means the Sonar desktop app was found.
	KindDesktopApp
	// KindSwiftTray means the legacy macOS menu bar binary was found.
	KindSwiftTray
)

func (k Kind) String() string {
	switch k {
	case KindDesktopApp:
		return "desktop-app"
	case KindSwiftTray:
		return "swift-tray"
	default:
		return "none"
	}
}

// Decision is the outcome of the lookup: what to launch, and where it is.
type Decision struct {
	Kind Kind
	Path string
}

// Env is the slice of the world the decision depends on, injected so the whole
// table can be exercised on one host against a fake filesystem.
type Env struct {
	GOOS     string
	Home     string
	SelfDir  string // directory holding the sonar binary
	Getenv   func(string) string
	Exists   func(string) bool
	LookPath func(string) (string, error)
}

// osEnv is the real environment.
func osEnv() Env {
	home, _ := os.UserHomeDir()
	selfDir := ""
	if self, err := os.Executable(); err == nil {
		selfDir = filepath.Dir(self)
	}
	return Env{
		GOOS:    runtime.GOOS,
		Home:    home,
		SelfDir: selfDir,
		Getenv:  os.Getenv,
		Exists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		LookPath: exec.LookPath,
	}
}

// Decide picks what `sonar tray` should launch. The order is deliberate: the
// desktop app wins wherever it is installed, because it is the tray from spec 4
// onwards; the Swift binary is only a fallback for machines that have not
// installed the app yet.
func Decide(env Env) Decision {
	if path, ok := findDesktopApp(env); ok {
		return Decision{Kind: KindDesktopApp, Path: path}
	}
	if env.GOOS == "darwin" {
		if path, ok := findSwiftTray(env); ok {
			return Decision{Kind: KindSwiftTray, Path: path}
		}
	}
	return Decision{Kind: KindNone}
}

// findDesktopApp looks in the places each platform's installer writes to. It is
// best effort by design: a user who installed the app somewhere unusual gets
// the download hint rather than a wrong guess.
func findDesktopApp(env Env) (string, bool) {
	for _, candidate := range desktopCandidates(env) {
		if candidate != "" && env.exists(candidate) {
			return candidate, true
		}
	}
	for _, name := range desktopBinaryNames(env.GOOS) {
		if path, ok := env.lookPath(name); ok {
			return path, true
		}
	}
	return "", false
}

func desktopCandidates(env Env) []string {
	switch env.GOOS {
	case "darwin":
		out := []string{"/Applications/Sonar.app"}
		if env.Home != "" {
			out = append(out, filepath.Join(env.Home, "Applications", "Sonar.app"))
		}
		return out
	case "windows":
		// The installer registers itself under the user's Programs directory;
		// a machine-wide install lands in Program Files. Reading the registry
		// would be more precise and is what a later step can do — these cover
		// what the NSIS and MSI bundles actually write.
		var out []string
		for _, base := range []string{
			env.getenv("LOCALAPPDATA"),
			env.getenv("ProgramFiles"),
			env.getenv("ProgramFiles(x86)"),
		} {
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, "Programs", "Sonar", "Sonar.exe"),
				filepath.Join(base, "Sonar", "Sonar.exe"))
		}
		return out
	default:
		out := []string{"/opt/Sonar/sonar-desktop", "/usr/bin/sonar-desktop", "/usr/local/bin/sonar-desktop"}
		if env.Home != "" {
			out = append(out, filepath.Join(env.Home, ".local", "bin", "sonar-desktop"))
		}
		return out
	}
}

// desktopBinaryNames is what to look for on PATH. macOS installs a bundle, not
// a bare binary, so there is nothing to look up there.
func desktopBinaryNames(goos string) []string {
	if goos == "darwin" {
		return nil
	}
	return []string{"sonar-desktop"}
}

// findSwiftTray keeps the pre-app behaviour: the binary shipped next to sonar,
// or one on PATH.
// LegacyTray reports whether the legacy macOS menu bar binary is installed on
// this machine, whatever else is. Decide prefers the desktop app and so never
// mentions a sonar-tray that sits beside it; `sonar doctor` wants to see the
// leftover exactly then, which is why this is its own lookup.
func LegacyTray() (string, bool) { return LegacyTrayIn(osEnv()) }

// LegacyTrayIn is LegacyTray against an injected environment.
func LegacyTrayIn(env Env) (string, bool) {
	if env.GOOS != "darwin" {
		return "", false
	}
	return findSwiftTray(env)
}

func findSwiftTray(env Env) (string, bool) {
	if env.SelfDir != "" {
		candidate := filepath.Join(env.SelfDir, "sonar-tray")
		if env.exists(candidate) {
			return candidate, true
		}
	}
	return env.lookPath("sonar-tray")
}

func (e Env) exists(path string) bool {
	if e.Exists == nil {
		return false
	}
	return e.Exists(path)
}

func (e Env) lookPath(name string) (string, bool) {
	if e.LookPath == nil {
		return "", false
	}
	path, err := e.LookPath(name)
	return path, err == nil
}

func (e Env) getenv(key string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(key)
}

// NotInstalledError is what `sonar tray` returns when there is nothing to
// launch. It is an error rather than a note so a script can tell the two apart,
// and it carries the download URL because that is the only useful next step.
type NotInstalledError struct{ GOOS string }

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("the Sonar desktop app is not installed — download it from %s", DownloadURL)
}

// Run launches whatever Decide found. detach only affects the Swift tray: the
// desktop app is a windowed application and is always started in the
// background.
func Run(detach bool) error { return run(Decide(osEnv()), detach) }

func run(d Decision, detach bool) error {
	switch d.Kind {
	case KindDesktopApp:
		return launchDesktopApp(d.Path)
	case KindSwiftTray:
		return launchSwiftTray(d.Path, detach)
	default:
		return &NotInstalledError{GOOS: runtime.GOOS}
	}
}

// launchDesktopApp starts the app without waiting for it. On macOS that means
// `open`, which hands the bundle to LaunchServices; elsewhere it is a plain
// detached exec.
func launchDesktopApp(path string) error {
	if runtime.GOOS == "darwin" && strings.HasSuffix(path, ".app") {
		cmd := exec.Command("open", "-a", path)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("launching %s: %w", path, err)
		}
		return nil
	}
	cmd := exec.Command(path)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching %s: %w", path, err)
	}
	return cmd.Process.Release()
}

// launchSwiftTray is the pre-app behaviour, unchanged: run it in the
// foreground, or start it in its own session with --detach.
func launchSwiftTray(path string, detach bool) error {
	cmd := exec.Command(path)
	if detach {
		detachProcess(cmd)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start tray: %w", err)
		}
		fmt.Fprintf(os.Stderr, "sonar tray running (pid %d)\n", cmd.Process.Pid)
		return nil
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
