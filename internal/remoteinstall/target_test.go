package remoteinstall

import (
	"errors"
	"strings"
	"testing"
)

func TestParsePlatformMapsUnameToAnAsset(t *testing.T) {
	cases := []struct {
		uname string
		os    string
		arch  string
		asset string
	}{
		{"Linux x86_64", "linux", "amd64", "sonar_linux_amd64.tar.gz"},
		{"Linux aarch64", "linux", "arm64", "sonar_linux_arm64.tar.gz"},
		{"Linux arm64", "linux", "arm64", "sonar_linux_arm64.tar.gz"},
		{"Darwin arm64", "darwin", "arm64", "sonar_darwin_arm64.tar.gz"},
		{"Darwin x86_64", "darwin", "amd64", "sonar_darwin_amd64.tar.gz"},
		// ssh folds the trailing newline in, and `uname -s` on some kernels
		// prints more than one word.
		{"  Linux x86_64\n", "linux", "amd64", "sonar_linux_amd64.tar.gz"},
	}
	for _, c := range cases {
		p, err := ParsePlatform(c.uname)
		if err != nil {
			t.Fatalf("ParsePlatform(%q): %v", c.uname, err)
		}
		if p.OS != c.os || p.Arch != c.arch {
			t.Errorf("ParsePlatform(%q) = %s/%s, want %s/%s", c.uname, p.OS, p.Arch, c.os, c.arch)
		}
		if got := p.Asset(); got != c.asset {
			t.Errorf("ParsePlatform(%q).Asset() = %s, want %s", c.uname, got, c.asset)
		}
	}
}

func TestParsePlatformRejectsUnsupportedHosts(t *testing.T) {
	cases := []struct {
		uname string
		want  string
	}{
		{"MINGW64_NT-10.0-22631 x86_64", "Windows"},
		{"FreeBSD amd64", "Linux and macOS"},
		{"Linux i686", "32-bit"},
		{"Linux armv7l", "x86_64 and aarch64"},
	}
	for _, c := range cases {
		_, err := ParsePlatform(c.uname)
		var unsupported *UnsupportedPlatformError
		if !errors.As(err, &unsupported) {
			t.Fatalf("ParsePlatform(%q) error = %v, want an UnsupportedPlatformError", c.uname, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ParsePlatform(%q) = %q, want it to mention %q", c.uname, err, c.want)
		}
	}
}

func TestParsePlatformRejectsEmptyUname(t *testing.T) {
	if _, err := ParsePlatform("   \n"); err == nil {
		t.Fatal("ParsePlatform(\"\") returned no error")
	}
}

func TestAssetURLPointsAtTheRelease(t *testing.T) {
	p := Platform{OS: "linux", Arch: "amd64"}
	want := "https://github.com/raskrebs/sonar/releases/download/v0.6.0/sonar_linux_amd64.tar.gz"
	if got := p.AssetURL("v0.6.0"); got != want {
		t.Errorf("AssetURL = %s, want %s", got, want)
	}
}
