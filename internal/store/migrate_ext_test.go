package store_test

// A sibling package filling in one of the reserved migration versions, exactly
// as spec 3 will for tunnels (cross-spec contract §8): register from outside
// internal/store, then open a store and find the table there.

import (
	"path/filepath"
	"testing"

	"github.com/raskrebs/sonar/internal/store"
)

func TestReservedMigrationFromAnotherPackage(t *testing.T) {
	before := store.LatestVersion()
	if before < store.VersionIndexes {
		t.Fatalf("LatestVersion = %d before registering, want at least %d", before, store.VersionIndexes)
	}

	store.RegisterMigration(store.VersionTunnels, "tunnels", `
		CREATE TABLE tunnels (
			id         TEXT PRIMARY KEY,
			port       INTEGER NOT NULL,
			url        TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`)

	if got := store.LatestVersion(); got != store.VersionTunnels {
		t.Fatalf("LatestVersion = %d after registering 003, want %d", got, store.VersionTunnels)
	}

	s, err := store.Open(filepath.Join(t.TempDir(), "sonar.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if v, err := s.Version(); err != nil || v != store.VersionTunnels {
		t.Fatalf("version = %d (%v), want %d", v, err, store.VersionTunnels)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO tunnels(id, port, url, created_at) VALUES('t1', 3000, 'https://x.example', '2026-01-01T00:00:00.000000000Z')`,
	); err != nil {
		t.Fatalf("writing to the externally registered table: %v", err)
	}

	// Registering the same version twice is refused, whoever asks.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a second registration of 003 did not panic")
			}
		}()
		store.RegisterMigration(store.VersionTunnels, "tunnels-again", "CREATE TABLE nope(x);")
	}()
}
