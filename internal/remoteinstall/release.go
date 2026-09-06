package remoteinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/raskrebs/sonar/internal/selfupdate"
)

// AssetResolver turns a release tag and an asset name into the URL the remote
// host downloads from and the sha256 it must match. It is a seam: the default
// asks GitHub, and the integration test serves a locally built archive.
type AssetResolver func(ctx context.Context, version, asset string) (url, sha256hex string, err error)

// versionPattern is the tag shape releases use ("v0.6.0", "v1.0.0-rc.1").
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.\-]+)?$`)

// ErrDevBuild is returned when the local binary carries no release version and
// the caller named none. Installing "dev" on a remote host is not a thing that
// can be downloaded, and silently installing "latest" instead would put a
// different sonar on the far end from the one the user is running.
var ErrDevBuild = errors.New(
	"this sonar is a development build, so there is no matching release to install on the remote host\n" +
		"hint: name one with `sonar remote install <target> --version vX.Y.Z`")

// ResolveVersion returns the release to install: the one the caller asked for,
// or the local binary's own so that local and remote match.
func ResolveVersion(requested string) (string, error) {
	v := strings.TrimSpace(requested)
	if v == "" {
		v = strings.TrimSpace(selfupdate.Version)
		if !versionPattern.MatchString(v) {
			return "", ErrDevBuild
		}
		return v, nil
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("%q is not a release tag; releases are tagged vX.Y.Z", requested)
	}
	return v, nil
}

// checksumAssets are the names a release might publish its sha256 list under.
// GitHub reports a per-asset digest of its own, which is what the resolver
// prefers; this is the fallback for a release made before it did, and the hook
// a future workflow that publishes a checksum file drops into without a code
// change.
var checksumAssets = []string{"sonar_checksums.txt", "checksums.txt", "SHA256SUMS", "sonar_SHA256SUMS.txt"}

type ghAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// GitHubResolver is the default AssetResolver: it reads the release by tag and
// takes the sha256 GitHub publishes with the asset, falling back to a checksum
// file in the same release.
func GitHubResolver(ctx context.Context, version, asset string) (string, string, error) {
	rel, err := fetchRelease(ctx, version)
	if err != nil {
		return "", "", err
	}

	var want *ghAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == asset {
			want = &rel.Assets[i]
			break
		}
	}
	if want == nil {
		return "", "", fmt.Errorf("release %s publishes no %s", version, asset)
	}

	if sum, ok := strings.CutPrefix(want.Digest, "sha256:"); ok && sum != "" {
		return want.URL, sum, nil
	}

	sum, err := checksumFromFile(ctx, rel, asset)
	if err != nil {
		return "", "", err
	}
	return want.URL, sum, nil
}

// apiBase is GitHub's API root. It is a variable so the tests can serve a
// release without reaching the network.
var apiBase = "https://api.github.com"

func fetchRelease(ctx context.Context, version string) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiBase, Repo, version)
	body, err := get(ctx, url)
	if err != nil {
		var status *statusError
		if errors.As(err, &status) && status.code == http.StatusNotFound {
			return nil, fmt.Errorf("there is no sonar release %s\nhint: https://github.com/%s/releases lists them",
				version, Repo)
		}
		return nil, err
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("reading the %s release: %w", version, err)
	}
	return &rel, nil
}

// checksumFromFile looks for a checksum list among the release's assets and
// returns the line for one asset. The format is sha256sum's own:
// "<hex>  <name>".
func checksumFromFile(ctx context.Context, rel *ghRelease, asset string) (string, error) {
	for _, name := range checksumAssets {
		for _, a := range rel.Assets {
			if a.Name != name {
				continue
			}
			body, err := get(ctx, a.URL)
			if err != nil {
				return "", fmt.Errorf("downloading %s: %w", name, err)
			}
			if sum := ParseChecksums(string(body))[asset]; sum != "" {
				return sum, nil
			}
			return "", fmt.Errorf("%s does not list %s", name, asset)
		}
	}
	return "", fmt.Errorf("release %s publishes no sha256 for %s, so the download cannot be verified",
		rel.TagName, asset)
}

// ParseChecksums reads a sha256sum-style list into name -> hex. Both the plain
// ("<hex>  <name>") and binary ("<hex> *<name>") forms are accepted, and a
// name with directory components is matched on its last element.
func ParseChecksums(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
			name = name[i+1:]
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

type statusError struct {
	code int
	url  string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("GET %s: %s", e.url, http.StatusText(e.code))
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sonar/"+selfupdate.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{code: resp.StatusCode, url: url}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
