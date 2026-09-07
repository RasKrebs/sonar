package desktop

import (
	"errors"
	"strings"
	"testing"
)

const sampleManifest = `{
  "version": "0.1.0-beta.1",
  "published_at": "2026-09-07T12:00:00Z",
  "notes_url": "https://example.com/notes",
  "platforms": {
    "darwin-aarch64": {
      "url": "https://example.com/Sonar_0.1.0-beta.1_aarch64-apple-darwin.app.tar.gz",
      "sha256": "aa",
      "size": 41234567
    },
    "darwin-x86_64": {"url": "Sonar_x86_64.app.tar.gz", "sha256": "bb", "size": 2},
    "linux-x86_64": {
      "url": "https://example.com/Sonar_x86_64.AppImage",
      "sha256": "cc",
      "size": 3,
      "deb": {"url": "https://example.com/sonar-desktop_amd64.deb", "sha256": "dd", "size": 4}
    }
  }
}`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.1.0-beta.1" {
		t.Errorf("version = %q", m.Version)
	}
	if m.NotesURL != "https://example.com/notes" {
		t.Errorf("notes_url = %q", m.NotesURL)
	}
	want := []string{"darwin-aarch64", "darwin-x86_64", "linux-x86_64"}
	got := m.PlatformKeys()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestParseManifestRejectsNonsense(t *testing.T) {
	cases := map[string]string{
		"not json":     `<html>404</html>`,
		"no version":   `{"platforms":{"darwin-aarch64":{"url":"u","sha256":"s"}}}`,
		"no platforms": `{"version":"1.0.0","platforms":{}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(body)); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestPickResolvesRelativeURLsAndTheDeb(t *testing.T) {
	m, err := ParseManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	const manifestURL = "https://cdn.example.com/desktop/desktop.json"

	p, err := m.Pick("darwin-x86_64", manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://cdn.example.com/desktop/Sonar_x86_64.app.tar.gz" {
		t.Errorf("relative url resolved to %q", p.URL)
	}

	linux, err := m.Pick("linux-x86_64", manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	if linux.Deb == nil || linux.Deb.SHA256 != "dd" {
		t.Errorf("deb = %+v, want the sub-key parsed", linux.Deb)
	}
	// An absolute url is left exactly as published.
	if linux.URL != "https://example.com/Sonar_x86_64.AppImage" {
		t.Errorf("absolute url rewritten to %q", linux.URL)
	}
}

func TestPickNamesWhatIsAvailable(t *testing.T) {
	m, _ := ParseManifest([]byte(sampleManifest))
	_, err := m.Pick("windows-x86_64", "https://example.com/desktop.json")
	var unsupported *UnsupportedPlatformError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want an UnsupportedPlatformError", err)
	}
	for _, key := range []string{"darwin-aarch64", "darwin-x86_64", "linux-x86_64"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not list %s", err, key)
		}
	}
}

func TestPlatformKey(t *testing.T) {
	cases := map[[2]string]string{
		{"darwin", "arm64"}:  "darwin-aarch64",
		{"darwin", "amd64"}:  "darwin-x86_64",
		{"linux", "amd64"}:   "linux-x86_64",
		{"linux", "arm64"}:   "linux-aarch64",
		{"windows", "amd64"}: "windows-x86_64",
	}
	for in, want := range cases {
		if got := PlatformKey(in[0], in[1]); got != want {
			t.Errorf("PlatformKey(%s, %s) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestManifestURL(t *testing.T) {
	cases := []struct {
		name, base, version, want string
	}{
		{
			name: "default base, latest",
			base: DefaultBase,
			want: DefaultBase + "/desktop.json",
		},
		{
			name:    "default base, pinned version moves to the tag",
			base:    DefaultBase,
			version: "0.1.0-beta.1",
			want:    "https://github.com/raskrebs/sonar-desktop-releases/releases/download/v0.1.0-beta.1/desktop.json",
		},
		{
			name:    "a leading v is not doubled",
			base:    DefaultBase,
			version: "v0.2.0",
			want:    "https://github.com/raskrebs/sonar-desktop-releases/releases/download/v0.2.0/desktop.json",
		},
		{
			name: "a website base",
			base: "https://example.com/desktop/",
			want: "https://example.com/desktop/desktop.json",
		},
		{
			name:    "a website base with a version",
			base:    "https://example.com/desktop",
			version: "0.1.0",
			want:    "https://example.com/desktop/0.1.0/desktop.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ManifestURL(tc.base, tc.version); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestResolveBaseOverrideOrder(t *testing.T) {
	cases := []struct{ flag, env, cfg, want string }{
		{flag: "https://flag", env: "https://env", cfg: "https://cfg", want: "https://flag"},
		{env: "https://env", cfg: "https://cfg", want: "https://env"},
		{cfg: "https://cfg", want: "https://cfg"},
		{want: DefaultBase},
		{cfg: "https://cfg/", want: "https://cfg"},
	}
	for _, tc := range cases {
		if got := ResolveBase(tc.flag, tc.env, tc.cfg); got != tc.want {
			t.Errorf("ResolveBase(%q, %q, %q) = %q, want %q", tc.flag, tc.env, tc.cfg, got, tc.want)
		}
	}
}

func TestSameVersionIgnoresTheV(t *testing.T) {
	if !SameVersion("v1.2.3", "1.2.3") {
		t.Error("v1.2.3 and 1.2.3 are the same version")
	}
	if SameVersion("", "") {
		t.Error("two unknown versions are not a match")
	}
	if SameVersion("1.2.3", "1.2.4") {
		t.Error("different versions matched")
	}
}
