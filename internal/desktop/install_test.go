package desktop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ------------------------------------------------------------ the fixture ---

// fakeRelease is a base URL serving a manifest and its artifacts, all built in
// the test: a real gzipped tar holding a real (if useless) Sonar.app, and a
// real AppImage. Nothing here reaches the network.
type fakeRelease struct {
	server *httptest.Server
	// files is what the server serves, by path.
	files map[string][]byte
	// manifest is edited by a test before the server is asked for it.
	manifest Manifest
	mu       sync.Mutex
	requests []string
}

func newFakeRelease(t *testing.T, version string) *fakeRelease {
	t.Helper()
	r := &fakeRelease{files: map[string][]byte{}}

	bundle := bundleTarball(t)
	appImage := []byte("#!/bin/sh\necho fake AppImage\n")
	deb := []byte("!<arch>\nfake deb\n")

	r.files["/Sonar.app.tar.gz"] = bundle
	r.files["/Sonar.AppImage"] = appImage
	r.files["/sonar-desktop.deb"] = deb

	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.URL.Path)
		r.mu.Unlock()

		// Only the two paths a real base serves: the latest manifest, and the
		// one under this release's version. Anything else 404s, which is what
		// a wrong --base or --version deserves.
		if req.URL.Path == "/"+ManifestName || req.URL.Path == "/"+r.manifest.Version+"/"+ManifestName {
			body, err := json.Marshal(r.manifest)
			if err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		if strings.HasSuffix(req.URL.Path, "/"+ChecksumsName) {
			_, _ = fmt.Fprintf(w, "%s  Sonar.app.tar.gz\n", sum(bundle))
			return
		}
		body, ok := r.files[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.server.Close)

	base := r.server.URL
	r.manifest = Manifest{
		Version:     version,
		PublishedAt: "2026-09-07T12:00:00Z",
		NotesURL:    base + "/notes",
		Platforms: map[string]Platform{
			"darwin-aarch64": {Artifact: Artifact{URL: base + "/Sonar.app.tar.gz", SHA256: sum(bundle), Size: int64(len(bundle))}},
			"darwin-x86_64":  {Artifact: Artifact{URL: base + "/Sonar.app.tar.gz", SHA256: sum(bundle), Size: int64(len(bundle))}},
			"linux-x86_64": {
				Artifact: Artifact{URL: base + "/Sonar.AppImage", SHA256: sum(appImage), Size: int64(len(appImage))},
				Deb:      &Artifact{URL: base + "/sonar-desktop.deb", SHA256: sum(deb), Size: int64(len(deb))},
			},
			"linux-aarch64": {Artifact: Artifact{URL: base + "/Sonar.AppImage", SHA256: sum(appImage), Size: int64(len(appImage))}},
		},
	}
	return r
}

// mutate edits one platform entry, which is how the checksum and size failures
// are staged: the bytes on the wire stay correct and the manifest lies.
func (r *fakeRelease) mutate(key string, fn func(*Platform)) {
	p := r.manifest.Platforms[key]
	fn(&p)
	r.manifest.Platforms[key] = p
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// bundleTarball builds a .app.tar.gz the way the desktop app's own release
// does: a top-level Sonar.app directory, an executable inside it, and the
// symlink every real bundle has, so the extractor is exercised on the shapes
// it will actually meet.
func bundleTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	dirs := []string{"Sonar.app/", "Sonar.app/Contents/", "Sonar.app/Contents/MacOS/", "Sonar.app/Contents/Resources/"}
	for _, d := range dirs {
		if err := tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]struct {
		body string
		mode int64
	}{
		"Sonar.app/Contents/Info.plist":          {"<plist/>\n", 0o644},
		"Sonar.app/Contents/MacOS/sonar-desktop": {"#!/bin/sh\nexit 0\n", 0o755},
		"Sonar.app/Contents/Resources/icon.icns": {"icns", 0o644},
	}
	for name, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: f.mode, Size: int64(len(f.body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: "Sonar.app/Contents/Current", Typeflag: tar.TypeSymlink, Linkname: "MacOS", Mode: 0o777,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ------------------------------------------------------------- the harness ---

// harness is one installer run with every seam pointed somewhere harmless.
type harness struct {
	opts     Options
	state    *MemoryStore
	release  *fakeRelease
	launched []string
	commands [][]string
	// runningPIDs is what pgrep reports; a test sets it to fake an open app.
	runningPIDs []int
	// quitAfter is how many pgrep calls it takes before the app "quits".
	quitAfter int
	pgrepCall int
}

func newHarness(t *testing.T, goos, goarch, version string) *harness {
	t.Helper()
	h := &harness{state: &MemoryStore{}, release: newFakeRelease(t, version)}
	home := t.TempDir()
	h.opts = Options{
		Base:     h.release.server.URL,
		GOOS:     goos,
		GOARCH:   goarch,
		Home:     home,
		TempDir:  t.TempDir(),
		NoLaunch: false,
		State:    h.state,
		LookPath: func(string) (string, error) { return "", errors.New("not installed") },
		Launch: func(_ context.Context, _ *Options, path string) error {
			h.launched = append(h.launched, path)
			return nil
		},
		Exec: func(_ context.Context, name string, args ...string) (string, error) {
			h.commands = append(h.commands, append([]string{name}, args...))
			if name == "pgrep" {
				h.pgrepCall++
				if len(h.runningPIDs) == 0 || (h.quitAfter > 0 && h.pgrepCall > h.quitAfter) {
					return "", errors.New("exit status 1")
				}
				return strings.Join(pidStrings(h.runningPIDs), "\n") + "\n", nil
			}
			return "", nil
		},
		// Never the real /Applications: a directory this test owns, which it
		// can also make read-only to exercise the fallback.
		systemApps: t.TempDir(),
	}
	return h
}

func (h *harness) run(t *testing.T) (*Result, error) {
	t.Helper()
	return Run(context.Background(), h.opts)
}

func (h *harness) mustRun(t *testing.T) *Result {
	t.Helper()
	res, err := h.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func (h *harness) ran(name string) bool {
	for _, c := range h.commands {
		if c[0] == name {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------- the tests ---

func TestInstallMacOSIntoATempApplicationsDir(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0-beta.1")
	apps := t.TempDir()
	h.opts.Dir = apps

	res := h.mustRun(t)

	if res.Action != ActionInstalled {
		t.Errorf("action = %q, want %q", res.Action, ActionInstalled)
	}
	if res.Platform != "darwin-aarch64" {
		t.Errorf("platform = %q", res.Platform)
	}
	app := filepath.Join(apps, BundleName)
	if res.Path != app {
		t.Errorf("path = %q, want %q", res.Path, app)
	}
	// The bundle's own directory is stripped: Contents sits directly inside.
	body, err := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "sonar-desktop"))
	if err != nil {
		t.Fatalf("the installed bundle has no executable: %v", err)
	}
	if !strings.Contains(string(body), "exit 0") {
		t.Errorf("executable = %q", body)
	}
	info, err := os.Stat(filepath.Join(app, "Contents", "MacOS", "sonar-desktop"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode = %v, want the executable bit preserved", info.Mode())
	}
	if target, err := os.Readlink(filepath.Join(app, "Contents", "Current")); err != nil || target != "MacOS" {
		t.Errorf("symlink = %q (%v), want MacOS", target, err)
	}

	// The bundle's own directory entry is consumed, not unpacked: no
	// Sonar.app/Sonar.app.
	if exists(filepath.Join(app, BundleName)) {
		t.Error("the bundle was nested inside itself")
	}

	// Nothing is left behind next to the bundle.
	entries, _ := os.ReadDir(apps)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".Sonar.app") {
			t.Errorf("leftover %s", e.Name())
		}
	}

	if h.state.State.Version != "0.1.0-beta.1" || h.state.State.Path != app {
		t.Errorf("recorded %+v, want the version and path", h.state.State)
	}
	if len(h.launched) != 1 || h.launched[0] != app {
		t.Errorf("launched = %v, want [%s]", h.launched, app)
	}
	if !res.Launched {
		t.Error("result should say it launched")
	}
}

func TestInstallReplacesAnExistingAppAtomically(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.2.0")
	apps := t.TempDir()
	h.opts.Dir = apps

	// An older bundle, with a file the new one does not have: if the replace
	// merged instead of swapping, this would survive.
	old := filepath.Join(apps, BundleName)
	if err := os.MkdirAll(filepath.Join(old, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(old, "Contents", "MacOS", "leftover")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.state.State = State{Version: "0.1.0", Path: old}

	res := h.mustRun(t)

	if res.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", res.Action, ActionUpdated)
	}
	if res.PreviousVersion != "0.1.0" {
		t.Errorf("previous_version = %q", res.PreviousVersion)
	}
	if exists(stale) {
		t.Error("the old bundle's contents survived the replace")
	}
	if !exists(filepath.Join(old, "Contents", "Info.plist")) {
		t.Error("the new bundle is not in place")
	}
	if exists(filepath.Join(apps, "."+BundleName+".old")) {
		t.Error("the .old copy was not cleaned up")
	}
}

func TestInstallRefusesAChecksumMismatch(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	apps := t.TempDir()
	h.opts.Dir = apps
	h.release.mutate("darwin-aarch64", func(p *Platform) {
		p.SHA256 = strings.Repeat("0", 64)
	})

	_, err := h.run(t)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v, want a checksum failure", err)
	}
	if exists(filepath.Join(apps, BundleName)) {
		t.Error("a bundle was installed from an unverified download")
	}
	if h.state.State.Version != "" {
		t.Error("a failed install was recorded in the config")
	}
}

func TestInstallRefusesASizeMismatch(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	apps := t.TempDir()
	h.opts.Dir = apps
	h.release.mutate("darwin-aarch64", func(p *Platform) { p.Size = 999999 })

	_, err := h.run(t)
	if err == nil || !strings.Contains(err.Error(), "the manifest says") {
		t.Fatalf("err = %v, want a size mismatch", err)
	}
	if exists(filepath.Join(apps, BundleName)) {
		t.Error("a bundle was installed despite the size mismatch")
	}
}

func TestInstallRefusesAnArtifactWithNoChecksum(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	h.opts.Dir = t.TempDir()
	h.release.mutate("darwin-aarch64", func(p *Platform) { p.SHA256 = "" })

	_, err := h.run(t)
	if err == nil || !strings.Contains(err.Error(), "unverifiable") {
		t.Fatalf("err = %v, want a refusal to install something unverifiable", err)
	}
}

func TestInstallFallsBackToHomeApplications(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	// No --dir, and the stand-in for /Applications is read-only.
	if err := os.Chmod(h.opts.systemApps, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(h.opts.systemApps, 0o755) })

	res := h.mustRun(t)

	want := filepath.Join(h.opts.Home, UserApplications, BundleName)
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
	if !exists(filepath.Join(want, "Contents", "Info.plist")) {
		t.Error("nothing was installed into the fallback")
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "not writable") {
		t.Errorf("notes = %v, want one explaining the fallback", res.Notes)
	}
	if strings.Contains(strings.Join(res.Notes, " "), "sudo ") {
		t.Error("the fallback must never suggest sudo")
	}
}

func TestInstallRefusesWhileTheAppIsRunning(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	h.opts.Dir = t.TempDir()
	h.runningPIDs = []int{4242}

	_, err := h.run(t)
	var running *ErrRunning
	if !errors.As(err, &running) {
		t.Fatalf("err = %v, want an ErrRunning", err)
	}
	if !strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error %q should name the pid and --force", err)
	}
}

func TestForceAsksTheAppToQuit(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	h.opts.Dir = t.TempDir()
	h.opts.Force = true
	h.runningPIDs = []int{4242}
	h.quitAfter = 1 // it goes away after the first look

	h.mustRun(t)

	var quit []string
	for _, c := range h.commands {
		if c[0] == "osascript" {
			quit = c
		}
	}
	if len(quit) == 0 || !strings.Contains(strings.Join(quit, " "), `quit app "Sonar"`) {
		t.Errorf("commands = %v, want an osascript quit", h.commands)
	}
}

func TestCheckReportsBothVersionsAndInstallsNothing(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.3.0")
	apps := t.TempDir()
	h.opts.Dir = apps
	h.opts.Check = true

	t.Run("nothing installed", func(t *testing.T) {
		res := h.mustRun(t)
		if res.Action != ActionChecked {
			t.Errorf("action = %q", res.Action)
		}
		if res.InstalledVersion != "" {
			t.Errorf("installed_version = %q, want empty", res.InstalledVersion)
		}
		if res.Version != "0.3.0" {
			t.Errorf("version = %q", res.Version)
		}
		if res.UpToDate {
			t.Error("nothing installed is not up to date")
		}
		if exists(filepath.Join(apps, BundleName)) {
			t.Error("--check installed something")
		}
	})

	t.Run("current", func(t *testing.T) {
		app := filepath.Join(apps, BundleName)
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		h.state.State = State{Version: "0.3.0", Path: app}
		res := h.mustRun(t)
		if !res.UpToDate || res.InstalledVersion != "0.3.0" {
			t.Errorf("res = %+v, want up to date at 0.3.0", res)
		}
	})

	t.Run("a recorded path that is gone reads as not installed", func(t *testing.T) {
		h.state.State = State{Version: "0.3.0", Path: filepath.Join(apps, "Gone.app")}
		res := h.mustRun(t)
		if res.UpToDate || res.InstalledVersion != "" {
			t.Errorf("res = %+v, want not installed", res)
		}
	})
}

func TestUpdateIsANoOpOnTheCurrentVersion(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.4.0")
	apps := t.TempDir()
	h.opts.Dir = apps
	h.opts.Update = true

	app := filepath.Join(apps, BundleName)
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	h.state.State = State{Version: "v0.4.0", Path: app} // the v is decoration

	res := h.mustRun(t)

	if res.Action != ActionUpToDate {
		t.Fatalf("action = %q, want %q", res.Action, ActionUpToDate)
	}
	if len(h.launched) != 0 {
		t.Error("a no-op update should not launch the app")
	}
	for _, path := range h.release.requests {
		if !strings.HasSuffix(path, "/"+ManifestName) {
			t.Errorf("a no-op update downloaded %s", path)
		}
	}
}

func TestUpdateFromAnOlderVersionInstalls(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.5.0")
	apps := t.TempDir()
	h.opts.Dir = apps
	h.opts.Update = true

	app := filepath.Join(apps, BundleName)
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	h.state.State = State{Version: "0.4.0", Path: app}

	res := h.mustRun(t)
	if res.Action != ActionUpdated {
		t.Errorf("action = %q, want %q", res.Action, ActionUpdated)
	}
	if !exists(filepath.Join(app, "Contents", "Info.plist")) {
		t.Error("the new bundle is not in place")
	}
}

func TestNoLaunchInstallsWithoutOpening(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	h.opts.Dir = t.TempDir()
	h.opts.NoLaunch = true

	res := h.mustRun(t)
	if res.Launched || len(h.launched) != 0 {
		t.Errorf("--no-launch opened the app: %v", h.launched)
	}
}

func TestPinnedVersionAsksForThatManifest(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.9.0")
	h.opts.Dir = t.TempDir()
	h.opts.Version = "0.9.0"
	h.opts.NoLaunch = true

	h.mustRun(t)

	if len(h.release.requests) == 0 || h.release.requests[0] != "/0.9.0/"+ManifestName {
		t.Errorf("first request = %v, want /0.9.0/%s", h.release.requests, ManifestName)
	}
}

func TestUnsupportedPlatformNamesWhatIsPublished(t *testing.T) {
	h := newHarness(t, "darwin", "riscv64", "0.1.0")
	h.opts.Dir = t.TempDir()

	_, err := h.run(t)
	var unsupported *UnsupportedPlatformError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want an UnsupportedPlatformError", err)
	}
	if !strings.Contains(err.Error(), "darwin-aarch64") {
		t.Errorf("error %q should list the published platforms", err)
	}
}

func TestWindowsIsNotAvailableYet(t *testing.T) {
	_, err := Run(context.Background(), Options{GOOS: "windows", GOARCH: "amd64", State: &MemoryStore{}})
	var na *ErrNotAvailable
	if !errors.As(err, &na) {
		t.Fatalf("err = %v, want an ErrNotAvailable", err)
	}
	if !strings.Contains(err.Error(), "windows") {
		t.Errorf("error %q should name the platform", err)
	}
}

// ---------------------------------------------------------------- linux ---

func TestInstallLinuxPlacesTheAppImageAndWiresItIn(t *testing.T) {
	h := newHarness(t, "linux", "amd64", "0.1.0-beta.1")

	res := h.mustRun(t)

	appImage := filepath.Join(h.opts.Home, filepath.FromSlash(LinuxOptDir), AppImageName)
	if res.Path != appImage {
		t.Fatalf("path = %q, want %q", res.Path, appImage)
	}
	info, err := os.Stat(appImage)
	if err != nil {
		t.Fatalf("no AppImage: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}

	entry, err := os.ReadFile(filepath.Join(h.opts.Home, filepath.FromSlash(LinuxDesktopEntry)))
	if err != nil {
		t.Fatalf("no desktop entry: %v", err)
	}
	for _, want := range []string{"[Desktop Entry]", "Name=Sonar", "Exec=" + appImage, "Categories=Development;Utility;"} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("desktop entry is missing %q:\n%s", want, entry)
		}
	}

	link := filepath.Join(h.opts.Home, filepath.FromSlash(LinuxBinLink))
	target, err := os.Readlink(link)
	if err != nil || target != appImage {
		t.Errorf("symlink = %q (%v), want %s", target, err, appImage)
	}

	if h.state.State.Path != appImage || h.state.State.Version != "0.1.0-beta.1" {
		t.Errorf("recorded %+v", h.state.State)
	}
	if len(h.launched) != 1 {
		t.Errorf("launched = %v, want one launch", h.launched)
	}
}

func TestInstallLinuxRefreshesTheDesktopDatabaseWhenItExists(t *testing.T) {
	h := newHarness(t, "linux", "amd64", "0.1.0")
	h.opts.LookPath = func(name string) (string, error) {
		if name == "update-desktop-database" {
			return "/usr/bin/update-desktop-database", nil
		}
		return "", errors.New("not installed")
	}

	h.mustRun(t)

	if !h.ran("update-desktop-database") {
		t.Errorf("commands = %v, want update-desktop-database", h.commands)
	}
}

func TestInstallLinuxSkipsTheDesktopDatabaseWhenAbsent(t *testing.T) {
	h := newHarness(t, "linux", "amd64", "0.1.0")
	h.mustRun(t)
	if h.ran("update-desktop-database") {
		t.Error("ran update-desktop-database although it is not installed")
	}
}

func TestInstallLinuxReplacesAnExistingAppImage(t *testing.T) {
	h := newHarness(t, "linux", "amd64", "0.2.0")
	dir := filepath.Join(h.opts.Home, filepath.FromSlash(LinuxOptDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	appImage := filepath.Join(dir, AppImageName)
	if err := os.WriteFile(appImage, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.state.State = State{Version: "0.1.0", Path: appImage}

	res := h.mustRun(t)

	if res.Action != ActionUpdated {
		t.Errorf("action = %q", res.Action)
	}
	body, err := os.ReadFile(appImage)
	if err != nil || string(body) == "old" {
		t.Errorf("AppImage = %q (%v), want the new one", body, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+AppImageName) {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestInstallLinuxHonoursDir(t *testing.T) {
	h := newHarness(t, "linux", "arm64", "0.1.0")
	opt := t.TempDir()
	h.opts.Dir = opt

	res := h.mustRun(t)
	if res.Path != filepath.Join(opt, AppImageName) {
		t.Errorf("path = %q, want it under --dir", res.Path)
	}
	// The desktop entry still points at wherever the AppImage really is.
	entry, err := os.ReadFile(filepath.Join(h.opts.Home, filepath.FromSlash(LinuxDesktopEntry)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), "Exec="+res.Path) {
		t.Errorf("desktop entry does not point at %s:\n%s", res.Path, entry)
	}
}

func TestInstallLinuxRefusesWhileRunningAndTermsWithForce(t *testing.T) {
	h := newHarness(t, "linux", "amd64", "0.1.0")
	h.runningPIDs = []int{777}

	if _, err := h.run(t); !strings.Contains(fmt.Sprint(err), "777") {
		t.Fatalf("err = %v, want it to name the running pid", err)
	}
	// pgrep looks for the AppImage, not the macOS bundle path.
	for _, c := range h.commands {
		if c[0] == "pgrep" && c[2] != "Sonar.AppImage" {
			t.Errorf("pgrep pattern = %q, want Sonar.AppImage", c[2])
		}
	}

	h2 := newHarness(t, "linux", "amd64", "0.1.0")
	h2.runningPIDs = []int{777}
	h2.quitAfter = 1
	h2.opts.Force = true
	h2.mustRun(t)
	if !h2.ran("kill") {
		t.Errorf("commands = %v, want a kill -TERM", h2.commands)
	}
}

func TestDebIsRefusedOffLinuxAndWhenNotPublished(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	h.opts.Dir = t.TempDir()
	h.opts.Deb = true
	if _, err := h.run(t); err == nil || !strings.Contains(err.Error(), "Linux") {
		t.Errorf("err = %v, want --deb refused off Linux", err)
	}

	h2 := newHarness(t, "linux", "arm64", "0.1.0") // linux-aarch64 has no deb
	h2.opts.Deb = true
	if _, err := h2.run(t); err == nil || !strings.Contains(err.Error(), "no .deb") {
		t.Errorf("err = %v, want a missing-deb refusal", err)
	}
}

func TestAManifestThatIsNotThereSaysSo(t *testing.T) {
	h := newHarness(t, "darwin", "arm64", "0.1.0")
	h.opts.Base = h.release.server.URL + "/nope"
	h.opts.Version = "1.2.3"

	_, err := h.run(t)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404 that mentions --base or --version", err)
	}
}

func TestExtractRefusesAnArchiveThatEscapesTheDirectory(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "pwned"
	if err := tw.WriteHeader(&tar.Header{
		Name: "Sonar.app/../../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()

	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractBundle(archive, filepath.Join(dir, "dest")); err == nil {
		t.Fatal("want a refusal")
	}
}
