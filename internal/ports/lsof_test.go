package ports

import (
	"errors"
	"testing"
)

// capturedLsof is real `lsof -a -p … -d cwd -Fpn` output from a machine where
// one of the pids asked about had already exited: lsof reported what it could
// and exited non-zero.
const capturedLsof = `p71204
n/Users/me/src/storefront
p71209
n/Users/me/src/storefront/web
p980
n/
`

// nonZeroExit stands in for the *exec.ExitError that Output() returns when
// lsof exits non-zero; cwdsFromLsof only ever tests it for nil.
var nonZeroExit = errors.New("exit status 1")

func TestCwdsFromLsofKeepsWhatLsofFoundDespiteANonZeroExit(t *testing.T) {
	got := cwdsFromLsof([]byte(capturedLsof), nonZeroExit)

	want := map[int]string{
		71204: "/Users/me/src/storefront",
		71209: "/Users/me/src/storefront/web",
		980:   "/",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d cwds, want %d: %v", len(got), len(want), got)
	}
	for pid, cwd := range want {
		if got[pid] != cwd {
			t.Errorf("cwd of %d = %q, want %q", pid, got[pid], cwd)
		}
	}
}

func TestCwdsFromLsofParsesACleanRun(t *testing.T) {
	got := cwdsFromLsof([]byte("p1\nn/tmp\n"), nil)
	if got[1] != "/tmp" {
		t.Errorf("cwd of 1 = %q, want /tmp", got[1])
	}
}

func TestCwdsFromLsofGivesUpOnlyWhenLsofSaidNothing(t *testing.T) {
	if got := cwdsFromLsof(nil, nonZeroExit); len(got) != 0 {
		t.Errorf("got %v, want no cwds when lsof produced no output", got)
	}
	if got := cwdsFromLsof(nil, errors.New(`exec: "lsof": executable file not found`)); len(got) != 0 {
		t.Errorf("got %v, want no cwds when lsof is missing", got)
	}
}

func TestCwdsFromLsofIgnoresPathsWithNoProcess(t *testing.T) {
	// A path record before any process record, and a process record that is
	// not a number, must not attribute a cwd to pid 0 or to the pid before it.
	got := cwdsFromLsof([]byte("n/orphan\np12\nn/good\npNaN\nn/lost\n"), nil)
	if len(got) != 1 || got[12] != "/good" {
		t.Errorf("got %v, want only pid 12 -> /good", got)
	}
}
