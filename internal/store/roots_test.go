package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRootsRoundTrip(t *testing.T) {
	s := openTemp(t)

	roots, err := s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 0 {
		t.Fatalf("fresh store has roots %v", roots)
	}

	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	for _, p := range []string{a, b} {
		if err := s.AddRoot(p); err != nil {
			t.Fatalf("AddRoot(%s): %v", p, err)
		}
	}
	// Adding the same root twice is a no-op, trailing separators and all.
	if err := s.AddRoot(a + string(filepath.Separator)); err != nil {
		t.Fatalf("AddRoot (duplicate): %v", err)
	}

	roots, err = s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if want := []string{a, b}; !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots = %v, want %v", roots, want)
	}

	if err := s.RemoveRoot(a); err != nil {
		t.Fatalf("RemoveRoot: %v", err)
	}
	roots, _ = s.Roots()
	if want := []string{b}; !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots after remove = %v, want %v", roots, want)
	}
	// Removing an unknown root is not an error.
	if err := s.RemoveRoot(a); err != nil {
		t.Errorf("second RemoveRoot: %v", err)
	}
}

func TestRootsAreAbsolute(t *testing.T) {
	s := openTemp(t)

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := s.AddRoot("testdata"); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	roots, err := s.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if want := []string{filepath.Join(wd, "testdata")}; !reflect.DeepEqual(roots, want) {
		t.Errorf("Roots = %v, want %v", roots, want)
	}

	if err := s.AddRoot("  "); err == nil {
		t.Error("AddRoot accepted an empty path")
	}
	if err := s.RemoveRoot(""); err == nil {
		t.Error("RemoveRoot accepted an empty path")
	}
}

func TestRootsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sonar.db")
	root := t.TempDir()

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := first.AddRoot(root); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	roots, err := second.Roots()
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	if len(roots) != 1 || roots[0] != filepath.Clean(root) {
		t.Errorf("Roots after reopen = %v, want [%s]", roots, root)
	}
}
