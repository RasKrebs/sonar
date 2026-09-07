package desktop

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Where a Linux install puts its four pieces. All of them are under $HOME:
// this installer never needs root, so it never writes anywhere root owns, and
// a user who wants a system-wide install has --deb.
const (
	// LinuxOptDir holds the AppImage, relative to $HOME.
	LinuxOptDir = ".local/opt/sonar-desktop"
	// AppImageName is the AppImage's stable name. It is stable on purpose:
	// the .desktop entry and the ~/.local/bin symlink both point at it, so an
	// update replaces one file and nothing else has to be rewritten.
	AppImageName = "Sonar.AppImage"
	// LinuxDesktopEntry is the freedesktop launcher entry, relative to $HOME.
	LinuxDesktopEntry = ".local/share/applications/sonar-desktop.desktop"
	// LinuxBinLink is the command-line entry point, relative to $HOME.
	LinuxBinLink = ".local/bin/sonar-desktop"
)

// installLinux places the AppImage and wires it into the desktop.
//
// The AppImage itself is the whole app — one file, chmod 755 — so the atomic
// replace is a single rename rather than macOS's dance with a directory. The
// three things around it (the .desktop entry, the menu database, the symlink)
// are conveniences: each failure is a note, never an error, because an app
// that runs but has no menu icon is installed and an install that failed
// halfway is not.
func (o *Options) installLinux(ctx context.Context, archive string) (string, []string, error) {
	if o.Deb {
		return o.installDeb(ctx, archive)
	}
	if err := o.ensureNotRunning(ctx); err != nil {
		return "", nil, err
	}

	dir := o.Dir
	if dir == "" {
		if o.Home == "" {
			return "", nil, fmt.Errorf("no home directory to install into; pass --dir")
		}
		dir = filepath.Join(o.Home, filepath.FromSlash(LinuxOptDir))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating %s: %w", dir, err)
	}

	appImage := filepath.Join(dir, AppImageName)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d", AppImageName, os.Getpid()))
	// The download lives in TMPDIR, which is routinely a different filesystem
	// from $HOME, so it is copied in rather than renamed in: a cross-device
	// rename fails, and doing the copy first keeps the visible step atomic.
	if err := copyFile(archive, tmp, 0o755); err != nil {
		return "", nil, err
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", nil, err
	}
	if err := os.Rename(tmp, appImage); err != nil {
		return "", nil, fmt.Errorf("installing %s: %w", appImage, err)
	}

	var notes []string
	notes = append(notes, o.writeDesktopEntry(ctx, appImage)...)
	notes = append(notes, o.linkBin(appImage)...)
	return appImage, notes, nil
}

// writeDesktopEntry writes the launcher entry and refreshes the menu database.
//
// There is deliberately no Icon= line. The icon lives inside the AppImage, and
// the only way out of it is `./Sonar.AppImage --appimage-extract .DirIcon`,
// which means executing the binary that was just downloaded before the user
// has chosen to launch it, on a machine that may not even have FUSE. A missing
// icon is a generic tile in the menu; running the payload to get a picture is
// a worse trade.
func (o *Options) writeDesktopEntry(ctx context.Context, appImage string) []string {
	if o.Home == "" {
		return nil
	}
	path := filepath.Join(o.Home, filepath.FromSlash(LinuxDesktopEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return []string{"could not create " + filepath.Dir(path) + ": " + err.Error()}
	}
	entry := strings.Join([]string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=Sonar",
		"Comment=See and manage every service listening on a localhost port",
		"Exec=" + appImage,
		"Terminal=false",
		"Categories=Development;Utility;",
		"StartupWMClass=Sonar",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return []string{"could not write " + path + ": " + err.Error()}
	}

	// Best effort: a desktop that has no update-desktop-database either does
	// not need one or picks the entry up on its next scan.
	if _, err := o.LookPath("update-desktop-database"); err == nil {
		if _, err := o.Exec(ctx, "update-desktop-database", filepath.Dir(path)); err != nil {
			return []string{"update-desktop-database failed; the menu entry appears at next login"}
		}
	}
	return nil
}

// linkBin points ~/.local/bin/sonar-desktop at the AppImage, which is what
// `sonar tray` and anything else on PATH finds.
func (o *Options) linkBin(appImage string) []string {
	if o.Home == "" {
		return nil
	}
	link := filepath.Join(o.Home, filepath.FromSlash(LinuxBinLink))
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return []string{"could not create " + filepath.Dir(link) + ": " + err.Error()}
	}
	// Remove rather than overwrite: a symlink cannot be re-pointed in place,
	// and an existing regular file there is someone else's and worth saying so.
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return []string{link + " already exists and is not a symlink; left it alone"}
	}
	_ = os.Remove(link)
	if err := os.Symlink(appImage, link); err != nil {
		return []string{"could not link " + link + ": " + err.Error()}
	}
	return nil
}

// installDeb hands the verified package to the system package manager. This is
// the one path that needs root, and it only exists because the caller asked
// for it by name with --deb: a .deb install is system-wide by definition, so
// the "never sudo" rule that governs the AppImage does not apply. When there
// is no way to elevate, the verified file is kept and the exact command is
// printed rather than half-done.
func (o *Options) installDeb(ctx context.Context, archive string) (string, []string, error) {
	pkg := archive + ".deb" // apt and dpkg both insist on the extension
	if err := os.Rename(archive, pkg); err != nil {
		if err := copyFile(archive, pkg, 0o644); err != nil {
			return "", nil, err
		}
	}

	manager, args := o.debCommand()
	if manager == "" {
		return "", nil, fmt.Errorf("neither apt-get nor dpkg is installed; the verified package is at %s", pkg)
	}
	if os.Geteuid() != 0 {
		if _, err := o.LookPath("sudo"); err != nil {
			return "", nil, fmt.Errorf("installing a .deb needs root and sudo is not installed — run: %s %s %s",
				manager, strings.Join(args, " "), pkg)
		}
		args = append([]string{manager}, args...)
		manager = "sudo"
	}
	if out, err := o.Exec(ctx, manager, append(args, pkg)...); err != nil {
		return "", nil, fmt.Errorf("%s failed: %w\n%s", manager, err, strings.TrimSpace(out))
	}
	_ = os.Remove(pkg)

	// A package decides its own layout, so the installed path is looked up
	// rather than assumed.
	for _, candidate := range []string{"/usr/bin/sonar-desktop", "/opt/Sonar/sonar-desktop", "/usr/local/bin/sonar-desktop"} {
		if exists(candidate) {
			return candidate, nil, nil
		}
	}
	if path, err := o.LookPath("sonar-desktop"); err == nil {
		return path, nil, nil
	}
	return "", nil, fmt.Errorf("the package installed but sonar-desktop is not on PATH")
}

func (o *Options) debCommand() (string, []string) {
	if _, err := o.LookPath("apt-get"); err == nil {
		return "apt-get", []string{"install", "-y"}
	}
	if _, err := o.LookPath("dpkg"); err == nil {
		return "dpkg", []string{"-i"}
	}
	return "", nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return out.Close()
}
