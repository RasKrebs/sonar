package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// WAL means readers never block behind the writer. This drives both at once
// and fails on the first error either side sees.
func TestConcurrentReadersWhileWriting(t *testing.T) {
	s := openTemp(t)

	ctx, cancel := context.WithCancel(context.Background())
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
		reads   int
		writes  int
		readers = 4
	)
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	// One writer, hammering all three write paths.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for i := range 300 {
			if err := s.Append(HistoryEvent{
				Kind: EventPortUp, Port: 3000 + i%5, PID: i, DisplayName: "api", Group: "shop",
			}); err != nil {
				fail(err)
				return
			}
			if err := s.SetRename("port:3000", "api"); err != nil {
				fail(err)
				return
			}
			if err := s.AddRoot(filepath.Join("/code", "p")); err != nil {
				fail(err)
				return
			}
			mu.Lock()
			writes++
			mu.Unlock()
		}
	}()

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if _, err := s.Query(nil, time.Time{}, 20); err != nil {
					fail(err)
					return
				}
				if _, err := s.Renames(); err != nil {
					fail(err)
					return
				}
				if _, err := s.Roots(); err != nil {
					fail(err)
					return
				}
				mu.Lock()
				reads++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("concurrent access failed: %v", err)
	}
	if writes != 300 {
		t.Errorf("writer completed %d iterations, want 300", writes)
	}
	if reads == 0 {
		t.Error("no read completed while the writer was running")
	}
	if n, err := s.HistoryCount(); err != nil || n != 300 {
		t.Errorf("history holds %d rows (%v), want 300", n, err)
	}
}

// Two Stores on the same file (the daemon plus, say, a one-shot CLI) must not
// deadlock or lose writes.
func TestTwoStoresOnTheSameFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sonar.db")

	a, err := Open(path)
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(path)
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	defer func() { _ = b.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 50 {
				if err := s.Append(HistoryEvent{
					Kind: EventPortUp, Port: 3000 + i, PID: j,
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write from two stores: %v", err)
	}

	if n, err := a.HistoryCount(); err != nil || n != 100 {
		t.Errorf("history holds %d rows (%v), want 100", n, err)
	}
}
