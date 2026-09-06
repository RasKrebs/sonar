package relay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// openStores gives every storage test both backends: SQLite always, and
// Postgres when TEST_DATABASE_URL points at a throwaway database. The
// Postgres subtest drops the relay's tables first so a rerun starts clean.
func openStores(t *testing.T) map[string]Storage {
	t.Helper()
	out := map[string]Storage{}

	s, err := OpenSQLite(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("opening SQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	out["sqlite"] = s

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		return out
	}
	p, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("opening TEST_DATABASE_URL: %v", err)
	}
	truncatePostgres(t, p)
	t.Cleanup(func() { _ = p.Close() })
	out["postgres"] = p
	return out
}

func truncatePostgres(t *testing.T, s Storage) {
	t.Helper()
	ss, ok := s.(*sqlStore)
	if !ok {
		t.Fatal("expected a *sqlStore")
	}
	for _, table := range []string{"events", "installs", "daily_rollup"} {
		if _, err := ss.db.Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("clearing %s: %v", table, err)
		}
	}
}

func batchAt(id string, at time.Time, names ...string) Batch {
	b := Batch{InstallID: id, AppVersion: "v0.6.0", OS: "linux", Arch: "amd64"}
	for _, n := range names {
		b.Events = append(b.Events, Event{Name: n, At: at.UTC().Format(time.RFC3339)})
	}
	if err := ValidateBatch(&b, at); err != nil {
		panic(err)
	}
	return b
}

func idN(n int) string { return fmt.Sprintf("8f14e45f-ceea-467a-9c1b-2f0e1d3a4b%02d", n) }

func TestStorageRecordAndStats(t *testing.T) {
	for name, s := range openStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

			// One install today, one two days ago, one forty days ago.
			must(t, s.Record(ctx, "default", batchAt(idN(1), now, "app_open", "scan"), now))
			must(t, s.Record(ctx, "default", batchAt(idN(2), now.AddDate(0, 0, -2), "app_open"), now.AddDate(0, 0, -2)))
			must(t, s.Record(ctx, "default", batchAt(idN(3), now.AddDate(0, 0, -40), "app_open"), now.AddDate(0, 0, -40)))

			st, err := s.Stats(ctx, StatsQuery{Days: 30}, now)
			must(t, err)

			if st.Installs.Active1d != 1 {
				t.Errorf("active_1d = %d, want 1", st.Installs.Active1d)
			}
			if st.Installs.Active7d != 2 {
				t.Errorf("active_7d = %d, want 2", st.Installs.Active7d)
			}
			if st.Installs.Active30d != 2 {
				t.Errorf("active_30d = %d, want 2", st.Installs.Active30d)
			}
			if st.Installs.Total != 3 {
				t.Errorf("total = %d, want 3", st.Installs.Total)
			}

			today := now.UTC().Format("2006-01-02")
			if got := cell(st.Events, today, "app_open"); got.Count != 1 || got.Installs != 1 {
				t.Errorf("today's app_open = %+v, want count 1 installs 1", got)
			}
			if got := cell(st.Events, today, "scan"); got.Count != 1 {
				t.Errorf("today's scan = %+v, want count 1", got)
			}
			// The 40-day-old event is outside the 30-day window.
			old := now.AddDate(0, 0, -40).UTC().Format("2006-01-02")
			if got := cell(st.Events, old, "app_open"); got.Count != 0 {
				t.Errorf("a 40-day-old day leaked into a 30-day window: %+v", got)
			}
		})
	}
}

func TestStorageRecordUpsertsTheInstall(t *testing.T) {
	for name, s := range openStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			first := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
			later := time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)

			b := batchAt(idN(1), first, "app_open")
			b.AppVersion = "v0.5.0"
			must(t, s.Record(ctx, "default", b, first))

			b2 := batchAt(idN(1), later, "app_open")
			b2.AppVersion = "v0.6.0"
			must(t, s.Record(ctx, "default", b2, later))

			ss := s.(*sqlStore)
			var firstSeen, lastSeen, version string
			row := ss.db.QueryRow(`SELECT first_seen, last_seen, app_version FROM installs`)
			must(t, row.Scan(&firstSeen, &lastSeen, &version))
			if firstSeen != timeToString(first) {
				t.Errorf("first_seen moved: %s", firstSeen)
			}
			if lastSeen != timeToString(later) {
				t.Errorf("last_seen = %s, want %s", lastSeen, timeToString(later))
			}
			if version != "v0.6.0" {
				t.Errorf("app_version = %s, want the upgraded one", version)
			}

			st, err := s.Stats(ctx, StatsQuery{}, later)
			must(t, err)
			if st.Installs.Total != 1 {
				t.Errorf("an upsert created a second install: total = %d", st.Installs.Total)
			}
		})
	}
}

func TestRollupSurvivesRetention(t *testing.T) {
	for name, s := range openStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			old := now.AddDate(0, 0, -10)

			// Two installs on the same old day, so the rollup has an events
			// count and a distinct-installs count that differ.
			must(t, s.Record(ctx, "default", batchAt(idN(1), old, "app_open", "scan"), old))
			must(t, s.Record(ctx, "default", batchAt(idN(2), old, "app_open"), old))

			cells, err := s.Rollup(ctx)
			must(t, err)
			if cells == 0 {
				t.Fatal("the rollup wrote nothing")
			}

			oldDay := old.UTC().Format("2006-01-02")
			st, err := s.Stats(ctx, StatsQuery{Days: 30}, now)
			must(t, err)
			if got := cell(st.Events, oldDay, "app_open"); got.Count != 2 || got.Installs != 2 {
				t.Fatalf("before retention app_open = %+v, want count 2 installs 2", got)
			}

			// Retention removes the raw rows; the rollup must still answer.
			deleted, err := s.Retain(ctx, now.AddDate(0, 0, -5))
			must(t, err)
			if deleted != 3 {
				t.Errorf("deleted %d raw events, want 3", deleted)
			}

			st, err = s.Stats(ctx, StatsQuery{Days: 30}, now)
			must(t, err)
			if got := cell(st.Events, oldDay, "app_open"); got.Count != 2 || got.Installs != 2 {
				t.Fatalf("after retention app_open = %+v, want the rollup to still hold count 2 installs 2", got)
			}
			if got := cell(st.Events, oldDay, "scan"); got.Count != 1 {
				t.Fatalf("after retention scan = %+v, want count 1", got)
			}

			// A second rollup over an empty events table must not zero the
			// day it already summarised.
			if _, err := s.Rollup(ctx); err != nil {
				t.Fatal(err)
			}
			st, err = s.Stats(ctx, StatsQuery{Days: 30}, now)
			must(t, err)
			if got := cell(st.Events, oldDay, "app_open"); got.Count != 2 {
				t.Fatalf("a rollup after retention rewrote history: %+v", got)
			}
		})
	}
}

func TestRollupIsIdempotent(t *testing.T) {
	for name, s := range openStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			must(t, s.Record(ctx, "default", batchAt(idN(1), now, "app_open"), now))

			for i := 0; i < 3; i++ {
				if _, err := s.Rollup(ctx); err != nil {
					t.Fatal(err)
				}
			}
			st, err := s.Stats(ctx, StatsQuery{}, now)
			must(t, err)
			day := now.UTC().Format("2006-01-02")
			if got := cell(st.Events, day, "app_open"); got.Count != 1 {
				t.Fatalf("three rollups turned one event into %+v", got)
			}
		})
	}
}

func TestStatsFilterByProject(t *testing.T) {
	for name, s := range openStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
			must(t, s.Record(ctx, "acme", batchAt(idN(1), now, "app_open"), now))
			must(t, s.Record(ctx, "other", batchAt(idN(2), now, "app_open", "scan"), now))

			st, err := s.Stats(ctx, StatsQuery{Project: "acme"}, now)
			must(t, err)
			if st.Installs.Total != 1 {
				t.Errorf("acme total = %d, want 1", st.Installs.Total)
			}
			for _, c := range st.Events {
				if c.Project != "acme" {
					t.Errorf("project filter leaked %+v", c)
				}
			}

			all, err := s.Stats(ctx, StatsQuery{}, now)
			must(t, err)
			if all.Installs.Total != 2 {
				t.Errorf("unfiltered total = %d, want 2", all.Installs.Total)
			}
		})
	}
}

func TestSQLiteStoreCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "relay.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the database file was not created: %v", err)
	}
}

func TestRebindDollar(t *testing.T) {
	got := rebindDollar(`INSERT INTO t(a, b) VALUES(?, ?)`)
	want := `INSERT INTO t(a, b) VALUES($1, $2)`
	if got != want {
		t.Fatalf("rebindDollar = %q, want %q", got, want)
	}
}

func cell(cs []DayCount, day, name string) DayCount {
	for _, c := range cs {
		if c.Day == day && c.Name == name {
			return c
		}
	}
	return DayCount{}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
