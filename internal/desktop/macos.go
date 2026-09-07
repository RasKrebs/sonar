package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// SystemApplications is where a Mac app belongs, and UserApplications is where
// it goes when this user cannot write to the first. `sudo` is never an option:
// an app installed as root in a user's Applications folder is a support ticket
// waiting to happen, and a user who wants a machine-wide install can run this
// from an admin account.
const (
	SystemApplications = "/Applications"
	UserApplications   = "Applications" // under $HOME
)

// BundleName is the bundle this installs, everywhere it looks for one.
const BundleName = "Sonar.app"

// installMacOS unpacks the .app.tar.gz and swaps it into place.
//
// The swap is three renames inside one directory, so it is atomic from the
// point of view of anything opening the app: at no moment is there a
// half-written Sonar.app. Unpacking into <dir>/.Sonar.app.tmp-<pid> rather
// than a temp directory is what makes that true — a rename across filesystems
// is a copy, and a copy into /Applications is exactly the window this avoids.
//
// Nothing here touches com.apple.quarantine. A file this process downloaded
// never had the attribute (only apps that opt in set it), so the unsigned
// bundle opens without a Gatekeeper prompt, and a `xattr -d` would only teach
// the codebase a habit that becomes wrong the moment the app is notarised.
func (o *Options) installMacOS(ctx context.Context, archive string) (string, []string, error) {
	dir, notes, err := o.applicationsDir()
	if err != nil {
		return "", nil, err
	}
	if err := o.ensureNotRunning(ctx); err != nil {
		return "", nil, err
	}

	app := filepath.Join(dir, BundleName)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d", BundleName, os.Getpid()))
	old := filepath.Join(dir, "."+BundleName+".old")

	_ = os.RemoveAll(tmp)
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := extractBundle(archive, tmp); err != nil {
		return "", nil, err
	}
	if !exists(filepath.Join(tmp, "Contents", "MacOS")) {
		return "", nil, fmt.Errorf("the downloaded archive does not look like %s", BundleName)
	}

	_ = os.RemoveAll(old)
	hadOld := false
	if exists(app) {
		if err := os.Rename(app, old); err != nil {
			return "", nil, fmt.Errorf("moving the existing %s aside: %w", app, err)
		}
		hadOld = true
	}
	if err := os.Rename(tmp, app); err != nil {
		if hadOld {
			_ = os.Rename(old, app)
		}
		return "", nil, fmt.Errorf("installing %s: %w", app, err)
	}
	if hadOld {
		_ = os.RemoveAll(old)
	}
	return app, notes, nil
}

// applicationsDir picks where the bundle goes. An explicit --dir is taken at
// its word and fails loudly if it cannot be written; the default falls back to
// ~/Applications, which is a real macOS location LaunchServices indexes, with
// a note saying so.
func (o *Options) applicationsDir() (string, []string, error) {
	if o.Dir != "" {
		if err := os.MkdirAll(o.Dir, 0o755); err != nil {
			return "", nil, fmt.Errorf("creating %s: %w", o.Dir, err)
		}
		if !writable(o.Dir) {
			return "", nil, fmt.Errorf("%s is not writable", o.Dir)
		}
		return o.Dir, nil, nil
	}

	system := o.systemApps
	if system == "" {
		system = SystemApplications
	}
	if exists(system) && writable(system) {
		return system, nil, nil
	}

	if o.Home == "" {
		return "", nil, fmt.Errorf("%s is not writable and there is no home directory to fall back to", system)
	}
	fallback := filepath.Join(o.Home, UserApplications)
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating %s: %w", fallback, err)
	}
	if !writable(fallback) {
		return "", nil, fmt.Errorf("neither %s nor %s is writable", system, fallback)
	}
	return fallback, []string{fmt.Sprintf(
		"%s is not writable, so Sonar went to %s instead (it works the same; sonar never uses sudo)",
		system, fallback)}, nil
}
