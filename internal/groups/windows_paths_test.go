package groups

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Until the PEB reader landed, no port on Windows had a cwd, so nothing in this
// package was ever asked a question shaped like `C:\src\api`. These tests ask
// it. The first two run everywhere — filepath is the platform's own separator,
// so on the Windows runner they are the drive-letter case and on macOS they are
// the regression guard — and the third is the literal backslash table, which
// only means anything where backslash is the separator.

// TestFindAcceptsEverySpellingOfTheSameCheckout: the scanner's cwd and a
// caller's own directory reach Find spelled differently — with a trailing
// separator, with a redundant `.`, through a nested subdirectory — and all of
// them have to name one root, or one project becomes several groups.
func TestFindAcceptsEverySpellingOfTheSameCheckout(t *testing.T) {
	base := tempTree(t)
	clone := mkdir(t, base, "api")
	mkdir(t, clone, ".git")
	deep := mkdir(t, clone, "services", "web")

	spellings := []string{
		clone,
		clone + string(filepath.Separator),
		filepath.Join(clone, "."),
		deep,
		deep + string(filepath.Separator),
		filepath.Join(deep, "..", "..", "services", "web"),
	}
	for _, cwd := range spellings {
		root, worktree, ok := Find(cwd)
		if !ok {
			t.Errorf("Find(%q) found no checkout", cwd)
			continue
		}
		if root != clone {
			t.Errorf("Find(%q) root = %q, want %q", cwd, root, clone)
		}
		if worktree != "" {
			t.Errorf("Find(%q) worktree = %q, want none", cwd, worktree)
		}
		if got := GroupName(root, worktree); got != "api" {
			t.Errorf("GroupName for %q = %q, want %q", cwd, got, "api")
		}
	}
}

// TestCanonicalCollapsesTheSpellingsToOneKey: the group index is a map keyed by
// directory, so two spellings of one directory are two groups. Canonical is the
// only thing standing between the PEB's spelling and that split.
func TestCanonicalCollapsesTheSpellingsToOneKey(t *testing.T) {
	base := tempTree(t)
	dir := mkdir(t, base, "api", "services", "web")

	want := Canonical(dir)
	if want == "" {
		t.Fatal("Canonical returned nothing for a directory that exists")
	}
	if strings.HasSuffix(want, string(filepath.Separator)) && filepath.Dir(want) != want {
		t.Errorf("Canonical(%q) = %q, which keeps a trailing separator", dir, want)
	}
	for _, spelling := range []string{
		dir + string(filepath.Separator),
		filepath.Join(dir, "."),
		filepath.Join(dir, "..", "web"),
	} {
		if got := Canonical(spelling); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", spelling, got, want)
		}
	}
}

// TestWindowsPathShapes is the drive-letter and backslash table. filepath's
// behaviour for `C:\…` only exists on Windows — elsewhere a backslash is an
// ordinary character in a file name — so this is the assertion the runner makes
// and the developer machine cannot.
func TestWindowsPathShapes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive letters and backslashes are only path syntax on Windows")
	}
	base := tempTree(t)
	clone := mkdir(t, base, "api")
	mkdir(t, clone, ".git")
	deep := mkdir(t, clone, "services", "web")

	if !strings.Contains(clone, `\`) || filepath.VolumeName(clone) == "" {
		t.Fatalf("the temp checkout %q is not a drive-letter path", clone)
	}

	// The PEB hands back a backslash path; a config or an env var may hand back
	// the same directory with forward slashes, and Windows accepts both.
	forward := strings.ReplaceAll(deep, `\`, `/`)
	if got, want := Canonical(forward), Canonical(deep); got != want {
		t.Errorf("Canonical(%q) = %q, want %q — forward slashes are a second key", forward, got, want)
	}
	if got := Canonical(deep); strings.Contains(got, "/") {
		t.Errorf("Canonical(%q) = %q, which still contains a forward slash", deep, got)
	}

	// The drive letter's case is not a second project either.
	lower := strings.ToLower(clone[:1]) + clone[1:]
	upper := strings.ToUpper(clone[:1]) + clone[1:]
	if got, want := Canonical(lower), Canonical(upper); got != want {
		t.Errorf("Canonical(%q) = %q but Canonical(%q) = %q", lower, got, upper, want)
	}

	// Find walks up with filepath.Dir, which has to stop at the drive root
	// rather than loop, and GroupName has to take the directory name off a
	// backslash path.
	root, worktree, ok := Find(forward)
	if !ok || root != Canonical(clone) {
		t.Errorf("Find(%q) = %q, %q, %v; want %q", forward, root, worktree, ok, Canonical(clone))
	}
	if got := GroupName(root, worktree); got != "api" {
		t.Errorf("GroupName(%q) = %q, want %q", root, got, "api")
	}

	// A directory that is not in any checkout still terminates: the walk from a
	// drive root has nowhere left to go.
	outside := filepath.VolumeName(clone) + `\`
	if _, _, ok := Find(outside); ok {
		if _, err := os.Stat(filepath.Join(outside, ".git")); err != nil {
			t.Errorf("Find(%q) claimed a checkout at the drive root", outside)
		}
	}
}

// TestWindowsWorktreeGitFile: a linked worktree's `.git` file holds a backslash
// gitdir on Windows, and the `<repo>@<worktree>` name is parsed straight out of
// it.
func TestWindowsWorktreeGitFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("backslash gitdir lines only occur on Windows")
	}
	base := tempTree(t)
	clone := mkdir(t, base, "sonar")
	mkdir(t, clone, ".git")
	wt := mkdir(t, base, "sonar-feature")
	writeFile(t, filepath.Join(wt, ".git"),
		"gitdir: "+filepath.Join(clone, ".git", "worktrees", "sonar-feature")+"\r\n")

	root, worktree, ok := Find(wt)
	if !ok {
		t.Fatalf("Find(%q) found no checkout", wt)
	}
	if root != Canonical(wt) {
		t.Errorf("root = %q, want %q", root, Canonical(wt))
	}
	if worktree != "sonar@sonar-feature" {
		t.Errorf("worktree = %q, want %q", worktree, "sonar@sonar-feature")
	}
	if got := GroupName(root, worktree); got != "sonar@sonar-feature" {
		t.Errorf("GroupName = %q, want %q", got, "sonar@sonar-feature")
	}
}
