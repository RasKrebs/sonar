// Package desktop installs the Sonar desktop app from a published manifest.
//
// The app is not signed by Apple yet, which is the whole reason this exists:
// a tester who downloads a .app.tar.gz in a browser gets a quarantine
// attribute on the bundle and Gatekeeper refuses to open an adhoc-signed app.
// A download made by this process gets no quarantine attribute at all, because
// only the programs that opt in (browsers, mail clients) set one. So the CLI
// neither sets nor strips com.apple.quarantine — there is nothing to strip,
// and a `xattr -d` here would be a habit that outlives the reason for it.
//
// The layout it reads is deliberately dumb: one JSON manifest and a checksums
// file beside it, both served from a plain base URL. Today that base is a
// GitHub release that carries only artifacts; tomorrow it can be a website
// with the same two names, and nothing here changes.
package desktop

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// DefaultBase is where the app is published: a public repository that holds
// artifacts and nothing else, so the CLI's own release page stays about the
// CLI.
const DefaultBase = "https://github.com/raskrebs/sonar-desktop-releases/releases/latest/download"

// BaseEnv overrides DefaultBase from the environment.
const BaseEnv = "SONAR_DESKTOP_BASE"

// ManifestName and ChecksumsName are the two files a base URL must serve.
// The checksums file is not read by the installer — the manifest already
// carries a sha256 per artifact — it is there so a human can verify a download
// by hand with `shasum -c`.
const (
	ManifestName  = "desktop.json"
	ChecksumsName = "sonar-desktop_checksums.txt"
)

// githubLatestSuffix is the tail of a GitHub "latest release" download base.
// A --version against such a base has to move to that repository's sibling
// tag URL rather than append a path segment.
const githubLatestSuffix = "/releases/latest/download"

// Artifact is one downloadable file: where it is, what it should hash to, and
// how big it should be. Both the size and the digest are checked, because they
// fail differently — a truncated transfer is a size mismatch long before it is
// a digest mismatch, and saying which one happened is the difference between
// "your network dropped" and "this file is not what the manifest describes".
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Platform is what one platform key resolves to: the artifact the installer
// uses by default, plus optionally a .deb for the Linux platforms, which
// `--deb` asks for instead.
type Platform struct {
	Artifact
	Deb *Artifact `json:"deb,omitempty"`
}

// Manifest is the whole desktop.json.
type Manifest struct {
	Version     string              `json:"version"`
	PublishedAt string              `json:"published_at"`
	NotesURL    string              `json:"notes_url"`
	Platforms   map[string]Platform `json:"platforms"`
}

// ParseManifest decodes and sanity-checks a desktop.json. It is strict about
// the two fields every later step depends on and silent about the rest: a
// manifest written by a newer publisher may carry keys this build does not
// know, and refusing it would make the CLI the thing that needs updating
// first.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", ManifestName, err)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, fmt.Errorf("%s has no version", ManifestName)
	}
	if len(m.Platforms) == 0 {
		return nil, fmt.Errorf("%s lists no platforms", ManifestName)
	}
	return &m, nil
}

// PlatformKeys lists the manifest's platforms in a stable order, so the
// "not supported yet" message reads the same on every run.
func (m *Manifest) PlatformKeys() []string {
	out := make([]string, 0, len(m.Platforms))
	for k := range m.Platforms {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Pick resolves the entry for a platform key, and turns every relative URL in
// it absolute against the manifest's own URL. Publishing absolute URLs is what
// the contract describes; resolving relative ones anyway means a static site
// can serve the same manifest from any prefix.
func (m *Manifest) Pick(key, manifestURL string) (Platform, error) {
	p, ok := m.Platforms[key]
	if !ok {
		return Platform{}, &UnsupportedPlatformError{Key: key, Available: m.PlatformKeys()}
	}
	var err error
	if p.URL, err = resolveURL(manifestURL, p.URL); err != nil {
		return Platform{}, err
	}
	if p.Deb != nil {
		deb := *p.Deb
		if deb.URL, err = resolveURL(manifestURL, deb.URL); err != nil {
			return Platform{}, err
		}
		p.Deb = &deb
	}
	return p, nil
}

func resolveURL(base, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("%s has an entry with no url", ManifestName)
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref, nil // a base we cannot parse cannot help; take the ref as-is
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%s has an unusable url %q: %w", ManifestName, ref, err)
	}
	return b.ResolveReference(r).String(), nil
}

// UnsupportedPlatformError is what a manifest that does not carry this machine
// says. It names what it does carry, because the useful next question is
// always "then what is published?".
type UnsupportedPlatformError struct {
	Key       string
	Available []string
}

func (e *UnsupportedPlatformError) Error() string {
	return fmt.Sprintf("%s is not supported yet — this release publishes %s",
		e.Key, strings.Join(e.Available, ", "))
}

// PlatformKey is the manifest key for a Go platform pair. The names are the
// Rust target vocabulary the desktop app's own build uses (aarch64, x86_64),
// not Go's, so the manifest reads the same as the file names beside it.
func PlatformKey(goos, goarch string) string {
	arch := goarch
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	case "386":
		arch = "i686"
	}
	return goos + "-" + arch
}

// ResolveBase applies the override order: an explicit --base, then
// SONAR_DESKTOP_BASE, then desktop.download_base from the user's config, then
// the built-in default.
func ResolveBase(flag, env, cfg string) string {
	for _, candidate := range []string{flag, env, cfg} {
		if v := strings.TrimSpace(candidate); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	return DefaultBase
}

// ManifestURL is where the manifest for a version lives under a base.
//
// With no --version it is simply <base>/desktop.json, which for the default
// base is the latest release's asset. With one, the shape depends on the base:
// a GitHub "latest release" URL has no room for a version segment, so the URL
// moves to that repository's sibling tag download path; any other base gets
// the plain <base>/<version>/desktop.json a static site can lay out.
func ManifestURL(base, version string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	version = strings.TrimSpace(version)
	if version == "" {
		return base + "/" + ManifestName
	}
	if prefix, ok := strings.CutSuffix(base, githubLatestSuffix); ok {
		tag := version
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		return prefix + "/releases/download/" + tag + "/" + ManifestName
	}
	return base + "/" + version + "/" + ManifestName
}

// SameVersion compares two version strings the way a user means it: a leading
// v is decoration, not part of the version.
func SameVersion(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	return a != "" && a == b
}
