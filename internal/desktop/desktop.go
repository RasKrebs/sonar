package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Actions a run reports.
const (
	ActionInstalled = "installed"
	ActionUpdated   = "updated"
	ActionUpToDate  = "up-to-date"
	ActionChecked   = "checked"
)

// QuitWait is how long --force waits for a running app to go away after it has
// been asked to quit.
const QuitWait = 10 * time.Second

// Exec runs an external command and returns its combined output. Every process
// this package starts goes through it, so a test can drive the whole installer
// on a machine that has neither `open` nor `pgrep -f Sonar.AppImage` matching
// anything real.
type Exec func(ctx context.Context, name string, args ...string) (string, error)

// Options is one `sonar install desktop` invocation.
type Options struct {
	// Base is the resolved download base; empty means DefaultBase.
	Base string
	// Version pins a release; empty means whatever the base serves.
	Version string
	// Dir overrides the install directory: the Applications directory on
	// macOS, the opt directory holding the AppImage on Linux.
	Dir string

	NoLaunch bool
	Force    bool
	Update   bool
	Check    bool
	Deb      bool

	// GOOS and GOARCH default to this build's, and are fields so the Linux
	// path can be exercised from a macOS test run and back.
	GOOS, GOARCH string
	// Home is the user's home directory; every path this package writes on
	// Linux, and the /Applications fallback on macOS, hangs off it.
	Home string
	// TempDir is where downloads land before they are verified.
	TempDir string

	// Out receives the human-readable notes and the progress line.
	Out io.Writer
	// Progress asks for a redrawing progress line; callers set it only on a
	// terminal.
	Progress bool

	HTTPClient *http.Client
	Exec       Exec
	LookPath   func(string) (string, error)
	// Launch starts the installed app. It is a seam because the two platforms
	// start an app in genuinely different ways and neither is something a test
	// should actually do.
	Launch func(ctx context.Context, opts *Options, path string) error
	// State reads and writes desktop.installed_* in the user's config.
	State StateStore

	// systemApps is /Applications, as a field so a test can point the
	// not-writable fallback at a directory it owns. Nothing outside this
	// package sets it, and nothing should: an explicit --dir is the supported
	// way to install somewhere else.
	systemApps string
}

// Result is what a run did, and the shape of `--json`.
type Result struct {
	Action   string `json:"action"`
	Platform string `json:"platform"`
	// Version is what the manifest offers.
	Version string `json:"version"`
	// InstalledVersion is what is on the machine after the run — the same as
	// Version except for `--check`, where it is what was already there.
	InstalledVersion string `json:"installed_version"`
	// PreviousVersion is what was replaced, when anything was.
	PreviousVersion string `json:"previous_version,omitempty"`
	Path            string `json:"path,omitempty"`
	NotesURL        string `json:"notes_url,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	Launched        bool   `json:"launched"`
	// UpToDate is only meaningful for `--check` and `--update`.
	UpToDate bool `json:"up_to_date"`
	// Notes are the things a human should read: the ~/Applications fallback,
	// a skipped desktop-database refresh.
	Notes []string `json:"notes,omitempty"`
}

// ErrNotAvailable is the platforms sonar has no installer for at all. It is
// separate from UnsupportedPlatformError, which is a manifest that happens not
// to carry this machine.
type ErrNotAvailable struct{ GOOS string }

func (e *ErrNotAvailable) Error() string {
	return fmt.Sprintf("the desktop app is not available for %s yet", e.GOOS)
}

// ErrRunning is the app being open when it is about to be replaced.
type ErrRunning struct{ PIDs []int }

func (e *ErrRunning) Error() string {
	return fmt.Sprintf("Sonar is running (pid %s) — quit it first, or pass --force to have sonar ask it to quit",
		strings.Join(pidStrings(e.PIDs), ", "))
}

func pidStrings(pids []int) []string {
	out := make([]string, 0, len(pids))
	for _, p := range pids {
		out = append(out, strconv.Itoa(p))
	}
	return out
}

func (o *Options) fill() {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.GOARCH == "" {
		o.GOARCH = runtime.GOARCH
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.HTTPClient == nil {
		o.HTTPClient = newHTTPClient()
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Exec == nil {
		o.Exec = runCommand
	}
	if o.Launch == nil {
		o.Launch = launch
	}
	if o.State == nil {
		o.State = ConfigStore{}
	}
	if o.Base == "" {
		o.Base = DefaultBase
	}
}

// runCommand is the real Exec.
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Supported reports whether this package can install the app for a GOOS.
// Windows is deliberately absent: the app has an installer of its own there
// and no manifest entry yet, so the command builds and refuses rather than
// pretending.
func Supported(goos string) bool { return goos == "darwin" || goos == "linux" }

// Run performs one invocation: fetch the manifest, decide, and either report
// (`--check`), do nothing (`--update` on the current version) or download,
// verify, install, record and launch.
func Run(ctx context.Context, opts Options) (*Result, error) {
	o := &opts
	o.fill()

	if !Supported(o.GOOS) {
		return nil, &ErrNotAvailable{GOOS: o.GOOS}
	}

	key := PlatformKey(o.GOOS, o.GOARCH)
	manifestURL := ManifestURL(o.Base, o.Version)
	manifest, err := o.fetchManifest(ctx, manifestURL)
	if err != nil {
		return nil, err
	}
	platform, err := manifest.Pick(key, manifestURL)
	if err != nil {
		return nil, err
	}

	state, err := o.State.Load()
	if err != nil {
		return nil, err
	}
	// A recorded version whose bundle has since been dragged to the trash is
	// not installed. Trusting the config alone would make `--update` a no-op
	// on a machine with no app on it.
	if state.Path != "" && !exists(state.Path) {
		state = State{}
	}

	res := &Result{
		Action:           ActionInstalled,
		Platform:         key,
		Version:          manifest.Version,
		InstalledVersion: state.Version,
		PreviousVersion:  state.Version,
		Path:             state.Path,
		NotesURL:         manifest.NotesURL,
		PublishedAt:      manifest.PublishedAt,
		UpToDate:         state.Version != "" && SameVersion(state.Version, manifest.Version),
	}

	if o.Check {
		res.Action = ActionChecked
		return res, nil
	}
	if o.Update && res.UpToDate {
		res.Action = ActionUpToDate
		return res, nil
	}

	art := platform.Artifact
	if o.Deb {
		if o.GOOS != "linux" {
			return nil, errors.New("--deb is a Linux option")
		}
		if platform.Deb == nil {
			return nil, fmt.Errorf("this release publishes no .deb for %s", key)
		}
		art = *platform.Deb
	}

	archive, err := o.downloadArtifact(ctx, art, o.TempDir, "Sonar "+manifest.Version)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(archive) }()

	var path string
	var notes []string
	if o.GOOS == "darwin" {
		path, notes, err = o.installMacOS(ctx, archive)
	} else {
		path, notes, err = o.installLinux(ctx, archive)
	}
	if err != nil {
		return nil, err
	}

	res.Path = path
	res.Notes = notes
	res.InstalledVersion = manifest.Version
	res.UpToDate = true
	if state.Version != "" {
		res.Action = ActionUpdated
	}

	if err := o.State.Save(State{Version: manifest.Version, Path: path}); err != nil {
		// The app is installed; failing the command now would make a
		// successful install look like a failed one. Say it and carry on.
		res.Notes = append(res.Notes, "could not record the install in the config: "+err.Error())
	}

	if !o.NoLaunch {
		if err := o.Launch(ctx, o, path); err != nil {
			res.Notes = append(res.Notes, "could not launch the app: "+err.Error())
		} else {
			res.Launched = true
		}
	}
	return res, nil
}

// running returns the pids of a running desktop app, found by the command line
// each platform's app actually has.
func (o *Options) running(ctx context.Context) []int {
	pattern := runningPattern(o.GOOS)
	if pattern == "" {
		return nil
	}
	out, err := o.Exec(ctx, "pgrep", "-f", pattern)
	if err != nil {
		// pgrep exits 1 when nothing matches, which is the common case, and
		// exits 127 when it is not installed at all. Neither is worth
		// reporting: the worst outcome is replacing a bundle under a running
		// app, which macOS tolerates and the user notices on next launch.
		return nil
	}
	var pids []int
	for _, line := range strings.Fields(out) {
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

func runningPattern(goos string) string {
	switch goos {
	case "darwin":
		return "Sonar.app/Contents/MacOS/sonar-desktop"
	case "linux":
		return "Sonar.AppImage"
	default:
		return ""
	}
}

// ensureNotRunning is the gate in front of every replace. Without --force it
// refuses; with it, it asks the app to quit and waits.
func (o *Options) ensureNotRunning(ctx context.Context) error {
	pids := o.running(ctx)
	if len(pids) == 0 {
		return nil
	}
	if !o.Force {
		return &ErrRunning{PIDs: pids}
	}
	o.note("asking Sonar to quit")
	o.quit(ctx, pids)

	deadline := time.Now().Add(QuitWait)
	for time.Now().Before(deadline) {
		if len(o.running(ctx)) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("Sonar did not quit within %s — quit it by hand and run this again", QuitWait)
}

// quit asks the app to go away the way its platform expects: on macOS through
// AppleScript, which gives the app the same "quit" a user's ⌘Q does, and on
// Linux with a TERM to each pid, which is what an AppImage understands.
func (o *Options) quit(ctx context.Context, pids []int) {
	if o.GOOS == "darwin" {
		_, _ = o.Exec(ctx, "osascript", "-e", `quit app "Sonar"`)
		return
	}
	args := append([]string{"-TERM"}, pidStrings(pids)...)
	_, _ = o.Exec(ctx, "kill", args...)
}

func (o *Options) note(format string, args ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", args...)
	}
}

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// writable reports whether a directory can be written to by this process. It
// answers by creating a file, because the mode bits lie on any machine with
// ACLs — and /Applications on a managed Mac is exactly such a machine.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".sonar-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}
