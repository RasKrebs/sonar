package remoteinstall

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/raskrebs/sonar/internal/selfupdate"
)

func TestResolveVersionDefaultsToThisBinarysRelease(t *testing.T) {
	restore := selfupdate.Version
	t.Cleanup(func() { selfupdate.Version = restore })

	selfupdate.Version = "v0.6.0"
	got, err := ResolveVersion("")
	if err != nil {
		t.Fatalf("ResolveVersion(\"\"): %v", err)
	}
	if got != "v0.6.0" {
		t.Errorf("ResolveVersion(\"\") = %s, want v0.6.0", got)
	}
}

// A dev build has no release to copy, and installing "latest" instead would put
// a different sonar on the far end from the one the user is running.
func TestResolveVersionRefusesToGuessForADevBuild(t *testing.T) {
	restore := selfupdate.Version
	t.Cleanup(func() { selfupdate.Version = restore })

	for _, v := range []string{"dev", "", "unknown", "0.6"} {
		selfupdate.Version = v
		_, err := ResolveVersion("")
		if !errors.Is(err, ErrDevBuild) {
			t.Errorf("ResolveVersion(\"\") with local version %q = %v, want ErrDevBuild", v, err)
		}
		if !strings.Contains(err.Error(), "--version") {
			t.Errorf("the dev-build error should say how to fix it, got %q", err)
		}
	}
}

func TestResolveVersionNormalisesAndValidatesATag(t *testing.T) {
	for in, want := range map[string]string{
		"v0.6.0":      "v0.6.0",
		"0.6.0":       "v0.6.0",
		" v1.0.0 ":    "v1.0.0",
		"v1.0.0-rc.1": "v1.0.0-rc.1",
	} {
		got, err := ResolveVersion(in)
		if err != nil {
			t.Fatalf("ResolveVersion(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ResolveVersion(%q) = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"latest", "main", "v1", "1.2", "v0.6.0/../etc"} {
		if _, err := ResolveVersion(bad); err == nil {
			t.Errorf("ResolveVersion(%q) returned no error", bad)
		}
	}
}

func TestParseChecksumsReadsBothSha256sumForms(t *testing.T) {
	body := `f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909  sonar_linux_amd64.tar.gz
E48570A81434686696DCEF983F4758C8C3A7ED2AAD4E4F159106257B51AED365 *dist/sonar_linux_arm64.tar.gz

# a comment line is ignored
`
	got := ParseChecksums(body)
	if got["sonar_linux_amd64.tar.gz"] != "f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909" {
		t.Errorf("amd64 sum = %q", got["sonar_linux_amd64.tar.gz"])
	}
	if got["sonar_linux_arm64.tar.gz"] != "e48570a81434686696dcef983f4758c8c3a7ed2aad4e4f159106257b51aed365" {
		t.Errorf("arm64 sum = %q", got["sonar_linux_arm64.tar.gz"])
	}
}

// The checksum file the release workflow publishes is the fallback this
// resolver reads, so its exact shape is pinned here rather than only described:
// testdata/sonar_checksums.txt is a byte-for-byte copy of what
// `sha256sum sonar_*.tar.gz sonar_*.zip | LC_ALL=C sort -k2` writes in the
// release job — six archives, sha256sum's two-space form, sorted by filename,
// and nothing else.
func TestParseChecksumsReadsTheReleaseChecksumFile(t *testing.T) {
	body, err := os.ReadFile("testdata/sonar_checksums.txt")
	if err != nil {
		t.Fatal(err)
	}

	// The file the release publishes covers the six archives and nothing else:
	// not itself, and not the Swift tray binary inside the darwin tarballs.
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("checksum file has %d lines, want 6", len(lines))
	}
	prev := ""
	for i, line := range lines {
		hex, name, ok := strings.Cut(line, "  ")
		if !ok || len(hex) != 64 || strings.HasPrefix(name, " ") {
			t.Fatalf("line %d is not sha256sum's \"<hex>  <name>\" form: %q", i+1, line)
		}
		if name <= prev {
			t.Errorf("line %d is not sorted by filename: %q after %q", i+1, name, prev)
		}
		prev = name
	}

	got := ParseChecksums(string(body))
	for _, name := range []string{
		"sonar_darwin_amd64.tar.gz",
		"sonar_darwin_arm64.tar.gz",
		"sonar_linux_amd64.tar.gz",
		"sonar_linux_arm64.tar.gz",
		"sonar_windows_amd64.zip",
		"sonar_windows_arm64.zip",
	} {
		sum := got[name]
		if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
			t.Errorf("%s = %q, want a lowercase hex sha256", name, sum)
		}
	}
	if len(got) != 6 {
		t.Errorf("parsed %d entries, want 6: %v", len(got), got)
	}
}

// GitHub publishes a sha256 digest per release asset, which is what the
// resolver reads: it needs no checksum file in the release, and it travels over
// this machine's TLS rather than the remote host's.
func TestGitHubResolverPrefersTheAssetDigest(t *testing.T) {
	srv := releaseServer(t, `{"tag_name":"v0.6.0","assets":[
		{"name":"sonar_linux_amd64.tar.gz","browser_download_url":"https://example.test/a.tar.gz",
		 "digest":"sha256:f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909"}]}`)
	url, sum, err := resolveFrom(t, srv, "v0.6.0", "sonar_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if url != "https://example.test/a.tar.gz" {
		t.Errorf("url = %s", url)
	}
	if sum != "f39d4a5bae986a4cefcd927680b4b2f6bfa065cf488baffce40e8af39f59e909" {
		t.Errorf("sha256 = %s", sum)
	}
}

// A release cut before GitHub reported digests still has to be installable, so
// a checksum file in the same release is the fallback.
func TestGitHubResolverFallsBackToAChecksumFile(t *testing.T) {
	var checksumURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/raskrebs/sonar/releases/tags/v0.5.1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.5.1","assets":[
			{"name":"sonar_linux_amd64.tar.gz","browser_download_url":"https://example.test/a.tar.gz","digest":""},
			{"name":"sonar_checksums.txt","browser_download_url":"` + checksumURL + `"}]}`))
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("abc123  sonar_linux_amd64.tar.gz\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	checksumURL = srv.URL + "/checksums"

	_, sum, err := resolveFrom(t, srv, "v0.5.1", "sonar_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sum != "abc123" {
		t.Errorf("sha256 = %s, want abc123", sum)
	}
}

// Without a checksum from anywhere the install must fail, not proceed
// unverified.
func TestGitHubResolverRefusesAnAssetWithNoChecksum(t *testing.T) {
	srv := releaseServer(t, `{"tag_name":"v0.6.0","assets":[
		{"name":"sonar_linux_amd64.tar.gz","browser_download_url":"https://example.test/a.tar.gz"}]}`)
	if _, _, err := resolveFrom(t, srv, "v0.6.0", "sonar_linux_amd64.tar.gz"); err == nil {
		t.Fatal("resolve returned no error for an asset with no published sha256")
	} else if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error = %q, want it to say the download cannot be verified", err)
	}
}

func TestGitHubResolverReportsAMissingRelease(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, _, err := resolveFrom(t, srv, "v9.9.9", "sonar_linux_amd64.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "no sonar release v9.9.9") {
		t.Fatalf("error = %v, want it to name the missing release", err)
	}
}

func TestGitHubResolverReportsAMissingAsset(t *testing.T) {
	srv := releaseServer(t, `{"tag_name":"v0.6.0","assets":[
		{"name":"sonar_linux_arm64.tar.gz","browser_download_url":"https://example.test/a.tar.gz"}]}`)
	_, _, err := resolveFrom(t, srv, "v0.6.0", "sonar_linux_amd64.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "sonar_linux_amd64.tar.gz") {
		t.Fatalf("error = %v, want it to name the missing asset", err)
	}
}

func releaseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// resolveFrom points the resolver's API base at a test server.
func resolveFrom(t *testing.T, srv *httptest.Server, version, asset string) (string, string, error) {
	t.Helper()
	restore := apiBase
	t.Cleanup(func() { apiBase = restore })
	apiBase = srv.URL
	return GitHubResolver(context.Background(), version, asset)
}
