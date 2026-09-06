package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type IN ('table','trigger','index') AND name = ?`,
		name,
	).Scan(&n)
	if err != nil {
		t.Fatalf("querying sqlite_master for %q: %v", name, err)
	}
	return n > 0
}

func TestMigrateFromEmpty(t *testing.T) {
	s := openTemp(t)

	v, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != LatestVersion() {
		t.Errorf("version = %d, want LatestVersion %d", v, LatestVersion())
	}
	if v < VersionIndexes {
		t.Errorf("version = %d, want at least %d", v, VersionIndexes)
	}

	for _, object := range []string{
		"schema_version", "renames", "group_pins", "port_events",
		"port_events_ring", "known_roots",
		"idx_port_events_at", "idx_port_events_port_at",
	} {
		if !tableExists(t, s.DB(), object) {
			t.Errorf("%s missing after migrating from empty", object)
		}
	}
}

// TestMigrateFromV1 replays a database written by a sonar that only had
// migration 001 and checks that 002 lands on top without losing rows.
func TestMigrateFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sonar.db")

	fixture, err := os.ReadFile(filepath.Join("testdata", "v1_schema.sql"))
	if err != nil {
		t.Fatalf("reading the v1 fixture: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("creating the v1 database: %v", err)
	}
	if _, err := raw.Exec(string(fixture)); err != nil {
		t.Fatalf("applying the v1 fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing the v1 database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a v1 database: %v", err)
	}
	defer func() { _ = s.Close() }()

	if s.ResetHappened() {
		t.Fatal("a valid v1 database was treated as corrupt")
	}
	if v, err := s.Version(); err != nil || v != LatestVersion() {
		t.Fatalf("version after upgrade = %d (%v), want %d", v, err, LatestVersion())
	}
	if !tableExists(t, s.DB(), "known_roots") {
		t.Error("known_roots missing after the v1 -> v2 upgrade")
	}

	// v1 data survived.
	if name, ok, err := s.GetRename("cwd:/home/me/code/shop:3000"); err != nil || !ok || name != "storefront" {
		t.Errorf("v1 rename after upgrade = %q, %v, %v; want storefront, true, nil", name, ok, err)
	}
	if group, ok, err := s.GetPin("port:9999"); err != nil || !ok || group != "legacy" {
		t.Errorf("v1 pin after upgrade = %q, %v, %v; want legacy, true, nil", group, ok, err)
	}
	events, err := s.Query(nil, time.Time{}, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 || events[0].DisplayName != "storefront" {
		t.Errorf("v1 history after upgrade = %+v, want the seeded storefront event", events)
	}

	// Migration 001 is not replayed: schema_version keeps one row per version.
	var applied int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_version WHERE version = 1`).Scan(&applied); err != nil {
		t.Fatalf("counting schema_version rows for 001: %v", err)
	}
	if applied != 1 {
		t.Errorf("schema_version has %d rows for version 1, want 1", applied)
	}
}

func TestRegisterMigrationRefusesDuplicates(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("re-registering version 1 did not panic")
		}
	}()
	RegisterMigration(VersionCore, "core-again", "CREATE TABLE nope(x);")
}

func TestRegisterMigrationRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		version int
		mname   string
		sql     string
	}{
		{"zero version", 0, "x", "CREATE TABLE x(y);"},
		{"negative version", -1, "x", "CREATE TABLE x(y);"},
		{"no name", 900, "  ", "CREATE TABLE x(y);"},
		{"no sql", 901, "x", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			RegisterMigration(tc.version, tc.mname, tc.sql)
		})
	}
}

func TestParseMigrationName(t *testing.T) {
	v, name, err := parseMigrationName("002_indexes_roots.sql")
	if err != nil || v != 2 || name != "indexes_roots" {
		t.Errorf("parseMigrationName = %d, %q, %v; want 2, indexes_roots, nil", v, name, err)
	}
	if _, _, err := parseMigrationName("nope.sql"); err == nil {
		t.Error("a file with no version prefix was accepted")
	}
	if _, _, err := parseMigrationName("abc_thing.sql"); err == nil {
		t.Error("a file with a non-numeric version was accepted")
	}
}

// TestReservedVersionsAreFree checks the embedded set, not the global
// registry: another package's test may legitimately have filled a reserved
// slot in by the time this runs.
func TestReservedVersionsAreFree(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	for _, e := range entries {
		v, _, err := parseMigrationName(e.Name())
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if owner, reserved := ReservedVersions[v]; reserved {
			t.Errorf("%s takes version %03d, reserved for %s", e.Name(), v, owner)
		}
	}
}

// A migration registered below the highest applied version is still applied:
// versions 003-006 belong to sibling packages, so a database can legitimately
// reach 006 in a build that has no 005 linked in.
func TestMigrateFillsAGapBelowTheHighestVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sonar.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Pretend a build with a later reserved migration has already run here.
	if _, err := s.DB().Exec(
		`INSERT INTO schema_version(version, name, applied_at) VALUES(?, 'later', ?)`,
		VersionClaims, nowString()); err != nil {
		t.Fatalf("recording a later version: %v", err)
	}
	// And that this build's migration 002 was never applied.
	if _, err := s.DB().Exec(`DELETE FROM schema_version WHERE version = ?`, VersionIndexes); err != nil {
		t.Fatalf("removing the 002 row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	applied, err := appliedVersions(reopened.DB())
	if err != nil {
		t.Fatalf("appliedVersions: %v", err)
	}
	if !applied[VersionIndexes] {
		t.Errorf("migration %03d was not applied under a higher version: %v", VersionIndexes, applied)
	}
	if !applied[VersionClaims] {
		t.Errorf("the pre-existing version row was lost: %v", applied)
	}
	if !tableExists(t, reopened.DB(), "known_roots") {
		t.Error("migration 002 did not run")
	}
}
