package claims

import "testing"

// The derived port is a contract between every sonar on the machine — two
// worktrees only agree because they both compute the same number — so the
// hash is pinned to golden values here. A change to fnv1a32, to the key shape
// or to DefaultRange moves every worktree's ports and must be a deliberate
// edit to this table, not a silent refactor.
func TestBaseIsPinnedAndProcessIndependent(t *testing.T) {
	// Golden numbers: computed once, then frozen. Two processes agree because
	// they both compute these, so a change here is a change every worktree on
	// the machine feels.
	golden := map[string]int{
		"sonar/main":        25900,
		"myapp/feature-x":   21700,
		"myapp/feature-y":   22190,
		"sonar/2A.6-claims": 14040,
		"":                  11110,
	}
	for key, want := range golden {
		if got := DefaultRange.Base(key); got != want {
			t.Errorf("Base(%q) = %d, want the pinned %d (the derivation moved)", key, got, want)
		}
	}
}

func TestBaseIsInsideTheRangeAndOnABlockBoundary(t *testing.T) {
	for _, key := range []string{"a/b", "sonar/main", "x/y", "long-project-name/some-long-worktree-name"} {
		got := DefaultRange.Base(key)
		if got < DefaultRange.Start || got > DefaultRange.End {
			t.Errorf("Base(%q) = %d, outside %d-%d", key, got, DefaultRange.Start, DefaultRange.End)
		}
		if (got-DefaultRange.Start)%BlockSize != 0 {
			t.Errorf("Base(%q) = %d, not on a %d-port block boundary", key, got, BlockSize)
		}
	}
}

func TestDifferentWorktreesLandInDifferentBlocks(t *testing.T) {
	a := DefaultRange.Base(Key("myapp", "feature-x"))
	b := DefaultRange.Base(Key("myapp", "feature-y"))
	if a == b {
		t.Fatalf("two worktrees derived the same base port %d", a)
	}
}

func TestAtWalksUpwardAndWrapsInsideTheRange(t *testing.T) {
	r := Range{Start: 10000, End: 10009}
	key := "wrap/me"
	base := r.Base(key)
	seen := map[int]bool{}
	for i := 0; i < r.Span(); i++ {
		p := r.At(key, i)
		if p < r.Start || p > r.End {
			t.Fatalf("At(%d) = %d, outside %d-%d", i, p, r.Start, r.End)
		}
		if seen[p] {
			t.Fatalf("At(%d) = %d repeated before the range was exhausted", i, p)
		}
		seen[p] = true
	}
	if len(seen) != r.Span() {
		t.Fatalf("walked %d distinct ports, want the whole %d-port range", len(seen), r.Span())
	}
	if got := r.At(key, 1); got != r.Start+(base-r.Start+1)%r.Span() {
		t.Errorf("At(1) = %d, want the port after the base", got)
	}
}

func TestKeyAndSplitKey(t *testing.T) {
	cases := []struct{ project, worktree, key string }{
		{"sonar", "", "sonar/" + DefaultWorktree},
		{"sonar", "main", "sonar/main"},
		{" myapp ", " feature-x ", "myapp/feature-x"},
	}
	for _, c := range cases {
		if got := Key(c.project, c.worktree); got != c.key {
			t.Errorf("Key(%q, %q) = %q, want %q", c.project, c.worktree, got, c.key)
		}
	}
	if p, w := SplitKey("myapp/feature-x"); p != "myapp" || w != "feature-x" {
		t.Errorf("SplitKey = %q, %q", p, w)
	}
	if p, w := SplitKey("myapp"); p != "myapp" || w != DefaultWorktree {
		t.Errorf("SplitKey(%q) = %q, %q", "myapp", p, w)
	}
}

func TestRangeValidation(t *testing.T) {
	if err := DefaultRange.Valid(); err != nil {
		t.Fatalf("DefaultRange is invalid: %v", err)
	}
	for _, r := range []Range{{0, 100}, {100, 70000}, {900, 800}} {
		if err := r.Valid(); err == nil {
			t.Errorf("%v was accepted", r)
		}
	}
}
