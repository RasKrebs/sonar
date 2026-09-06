package relay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // Postgres, when DATABASE_URL is set
	_ "modernc.org/sqlite"             // pure-Go SQLite, so CGO_ENABLED=0 holds
)

// timeLayout is the one spelling of time in the relay database: fixed-width
// UTC RFC 3339 with nine fractional digits, the same convention internal/store
// uses. Fixed width is what makes string comparison chronological, which is
// what lets retention, rollups and the stats window all be plain SQL ranges
// over a TEXT column — identical on SQLite and Postgres.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func timeToString(t time.Time) string { return t.UTC().Format(timeLayout) }

// dayOf is the UTC calendar day an instant falls in — the rollup's grain.
func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Storage is everything the HTTP layer needs from the database. One interface,
// two dialects: the SQL is written once and only the DDL and the placeholder
// style differ.
type Storage interface {
	// Record stores one validated batch and touches the install row.
	Record(ctx context.Context, project string, b Batch, receivedAt time.Time) error
	// Rollup recomputes daily_rollup for every day that still has raw events.
	Rollup(ctx context.Context) (int, error)
	// Retain deletes raw events older than cutoff and reports how many went.
	Retain(ctx context.Context, cutoff time.Time) (int64, error)
	// Stats answers GET /v1/stats.
	Stats(ctx context.Context, q StatsQuery, now time.Time) (Stats, error)
	Close() error
}

// StatsQuery narrows GET /v1/stats. A zero Days means 30.
type StatsQuery struct {
	Project string // "" means every project
	Days    int
}

// Stats is the body of GET /v1/stats.
type Stats struct {
	GeneratedAt string       `json:"generated_at"`
	Project     string       `json:"project,omitempty"`
	Days        int          `json:"days"`
	Installs    InstallStats `json:"installs"`
	Events      []DayCount   `json:"events"`
}

// InstallStats counts installs that reported in within each window.
type InstallStats struct {
	Active1d  int `json:"active_1d"`
	Active7d  int `json:"active_7d"`
	Active30d int `json:"active_30d"`
	Total     int `json:"total"`
}

// DayCount is one (day, name, project) cell of the rollup: how many events and
// how many distinct installs sent them.
type DayCount struct {
	Day      string `json:"day"`
	Name     string `json:"name"`
	Project  string `json:"project"`
	Count    int64  `json:"count"`
	Installs int64  `json:"installs"`
}

// dialect is the whole difference between SQLite and Postgres: how a
// placeholder is spelled, and how an auto-assigned id is declared. Every
// statement in this file is written once with `?` and rebound on the way out,
// which is what keeps the rollup, the retention window and the stats queries
// from forking.
type dialect struct {
	name       string
	rebind     func(string) string
	serialType string
}

func rebindDollar(q string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

func identity(q string) string { return q }

// schema is the relay's tables, written once with the serial type left open.
// There are no migrations yet: the relay is new, version 1 is what a fresh
// deployment gets, and relay_schema_version exists so the next change has
// somewhere to record itself.
func schema(serial string) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS relay_schema_version (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS installs (
			install_id     TEXT PRIMARY KEY,
			first_seen     TEXT NOT NULL,
			last_seen      TEXT NOT NULL,
			app_version    TEXT NOT NULL DEFAULT '',
			daemon_version TEXT NOT NULL DEFAULT '',
			os             TEXT NOT NULL DEFAULT '',
			arch           TEXT NOT NULL DEFAULT '',
			project        TEXT NOT NULL DEFAULT 'default'
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id          ` + serial + `,
			install_id  TEXT NOT NULL,
			name        TEXT NOT NULL,
			at          TEXT NOT NULL,
			received_at TEXT NOT NULL,
			project     TEXT NOT NULL DEFAULT 'default',
			props       TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS events_at ON events(at)`,
		`CREATE INDEX IF NOT EXISTS events_name_at ON events(name, at)`,
		`CREATE TABLE IF NOT EXISTS daily_rollup (
			day      TEXT   NOT NULL,
			name     TEXT   NOT NULL,
			project  TEXT   NOT NULL,
			count    BIGINT NOT NULL,
			installs BIGINT NOT NULL,
			PRIMARY KEY (day, name, project)
		)`,
		`CREATE INDEX IF NOT EXISTS installs_last_seen ON installs(last_seen)`,
	}
}

var (
	sqliteDialect = dialect{
		name:       "sqlite",
		rebind:     identity,
		serialType: "INTEGER PRIMARY KEY AUTOINCREMENT",
	}
	postgresDialect = dialect{
		name:       "postgres",
		rebind:     rebindDollar,
		serialType: "BIGSERIAL PRIMARY KEY",
	}
)

// sqlStore is the single implementation behind Storage. Writes are serialised
// in Go for SQLite, where a second writer is an error rather than a queue;
// Postgres does not need it, and the mutex is cheap enough to keep either way.
type sqlStore struct {
	db  *sql.DB
	d   dialect
	wmu sync.Mutex
}

// OpenSQLite opens (creating if needed) a relay database at path, in WAL mode
// with a busy timeout, exactly as internal/store does.
func OpenSQLite(path string) (Storage, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("relay: the SQLite path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return newSQLStore(db, sqliteDialect, path)
}

// OpenPostgres opens the database named by a libpq/pgx URL, which is what
// DATABASE_URL carries on every hosting provider worth using.
func OpenPostgres(url string) (Storage, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("opening the Postgres database: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return newSQLStore(db, postgresDialect, "postgres")
}

// Open picks the dialect the way `sonar relay serve` does: Postgres when a
// DATABASE_URL is given, SQLite otherwise.
func Open(databaseURL, sqlitePath string) (Storage, error) {
	if strings.TrimSpace(databaseURL) != "" {
		return OpenPostgres(databaseURL)
	}
	return OpenSQLite(sqlitePath)
}

func newSQLStore(db *sql.DB, d dialect, what string) (Storage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to %s (%s): %w", d.name, what, err)
	}
	for _, stmt := range schema(d.serialType) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("creating the relay schema: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, d.rebind(
		`INSERT INTO relay_schema_version(version, name, applied_at) VALUES(?, ?, ?)
		 ON CONFLICT (version) DO NOTHING`),
		1, "initial", timeToString(time.Now())); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("recording the relay schema version: %w", err)
	}
	return &sqlStore{db: db, d: d}, nil
}

func (s *sqlStore) Close() error { return s.db.Close() }

// Record writes the batch and the install row in one transaction: a batch is
// either counted or it is not, and a 202 promises it was.
func (s *sqlStore) Record(ctx context.Context, project string, b Batch, receivedAt time.Time) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	seen := timeToString(receivedAt)
	// One upsert covers "first ever batch" and "hundredth": first_seen is only
	// written by the insert, everything else is refreshed by the update, so an
	// install that upgrades reports its new version without losing its age.
	if _, err := tx.ExecContext(ctx, s.d.rebind(
		`INSERT INTO installs(install_id, first_seen, last_seen, app_version, daemon_version, os, arch, project)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (install_id) DO UPDATE SET
		   last_seen      = excluded.last_seen,
		   app_version    = excluded.app_version,
		   daemon_version = excluded.daemon_version,
		   os             = excluded.os,
		   arch           = excluded.arch,
		   project        = excluded.project`),
		b.InstallID, seen, seen, b.AppVersion, b.DaemonVersion, b.OS, b.Arch, project,
	); err != nil {
		return fmt.Errorf("recording the install: %w", err)
	}

	insert := s.d.rebind(
		`INSERT INTO events(install_id, name, at, received_at, project, props)
		 VALUES(?, ?, ?, ?, ?, ?)`)
	stmt, err := tx.PrepareContext(ctx, insert)
	if err != nil {
		return fmt.Errorf("preparing the event insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, e := range b.Events {
		at := receivedAt
		if e.At != "" {
			// Already validated as RFC 3339 by ValidateBatch.
			if t, perr := time.Parse(time.RFC3339, e.At); perr == nil {
				at = t
			}
		}
		if _, err := stmt.ExecContext(ctx,
			b.InstallID, e.Name, timeToString(at), seen, project, e.PropsJSON(),
		); err != nil {
			return fmt.Errorf("recording an event: %w", err)
		}
	}
	return tx.Commit()
}

// rollupSelect aggregates the raw events that are still on disk. substr() and
// COUNT(DISTINCT) mean the same thing in both dialects, and the fixed-width
// timestamp makes substr(at,1,10) the UTC day without a date function.
const rollupSelect = `
	SELECT substr(at, 1, 10) AS day, name, project, COUNT(*), COUNT(DISTINCT install_id)
	FROM events
	GROUP BY substr(at, 1, 10), name, project`

// Rollup recomputes daily_rollup from the raw events that remain and returns
// how many cells it wrote.
//
// It is a recompute, not an increment, which is what makes it safe to run on
// an hourly tick, safe to run twice, and safe to run after a crash: the answer
// only ever depends on the rows that are there. Retention then removes raw
// events without touching daily_rollup, so a day whose raw rows are gone keeps
// the totals this pass left behind — that is the point of the table.
func (s *sqlStore) Rollup(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, rollupSelect)
	if err != nil {
		return 0, fmt.Errorf("aggregating events: %w", err)
	}
	var cells []DayCount
	for rows.Next() {
		var c DayCount
		if err := rows.Scan(&c.Day, &c.Name, &c.Project, &c.Count, &c.Installs); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("aggregating events: %w", err)
		}
		cells = append(cells, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("aggregating events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	upsert := s.d.rebind(
		`INSERT INTO daily_rollup(day, name, project, count, installs)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT (day, name, project) DO UPDATE SET
		   count = excluded.count, installs = excluded.installs`)
	stmt, err := tx.PrepareContext(ctx, upsert)
	if err != nil {
		return 0, fmt.Errorf("preparing the rollup upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, c := range cells {
		if _, err := stmt.ExecContext(ctx, c.Day, c.Name, c.Project, c.Count, c.Installs); err != nil {
			return 0, fmt.Errorf("writing the rollup: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(cells), nil
}

// Retain deletes raw events older than cutoff. Rollups are never deleted: the
// aggregate is the thing worth keeping, and it is already anonymous.
func (s *sqlStore) Retain(ctx context.Context, cutoff time.Time) (int64, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.d.rebind(`DELETE FROM events WHERE at < ?`), timeToString(cutoff))
	if err != nil {
		return 0, fmt.Errorf("applying retention: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // not every driver reports it; the delete still happened
	}
	return n, nil
}

// Stats answers GET /v1/stats. Install counts come from installs.last_seen;
// the per-day event counts come from daily_rollup, with the days that still
// have raw events recomputed on the spot so "today" is never stale between
// hourly rollups.
func (s *sqlStore) Stats(ctx context.Context, q StatsQuery, now time.Time) (Stats, error) {
	days := q.Days
	if days <= 0 {
		days = 30
	}
	out := Stats{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Project:     q.Project,
		Days:        days,
	}

	windows := []struct {
		days int
		into *int
	}{{1, &out.Installs.Active1d}, {7, &out.Installs.Active7d}, {30, &out.Installs.Active30d}}
	for _, w := range windows {
		n, err := s.countInstalls(ctx, q.Project, timeToString(now.Add(-time.Duration(w.days)*24*time.Hour)))
		if err != nil {
			return Stats{}, err
		}
		*w.into = n
	}
	total, err := s.countInstalls(ctx, q.Project, "")
	if err != nil {
		return Stats{}, err
	}
	out.Installs.Total = total

	since := dayOf(now.Add(-time.Duration(days) * 24 * time.Hour))
	byKey := map[[3]string]DayCount{}

	add := func(c DayCount) {
		if c.Day < since {
			return
		}
		if q.Project != "" && c.Project != q.Project {
			return
		}
		byKey[[3]string{c.Day, c.Name, c.Project}] = c
	}

	rolled, err := s.queryCells(ctx, `SELECT day, name, project, count, installs FROM daily_rollup`)
	if err != nil {
		return Stats{}, err
	}
	for _, c := range rolled {
		add(c)
	}
	// Raw rows win: they are newer than whatever the last hourly pass wrote.
	live, err := s.queryCells(ctx, rollupSelect)
	if err != nil {
		return Stats{}, err
	}
	for _, c := range live {
		add(c)
	}

	out.Events = make([]DayCount, 0, len(byKey))
	for _, c := range byKey {
		out.Events = append(out.Events, c)
	}
	sortDayCounts(out.Events)
	return out, nil
}

func (s *sqlStore) countInstalls(ctx context.Context, project, since string) (int, error) {
	query := `SELECT COUNT(*) FROM installs`
	var args []any
	var where []string
	if since != "" {
		where = append(where, `last_seen >= ?`)
		args = append(args, since)
	}
	if project != "" {
		where = append(where, `project = ?`)
		args = append(args, project)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, s.d.rebind(query), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting installs: %w", err)
	}
	return n, nil
}

func (s *sqlStore) queryCells(ctx context.Context, query string) ([]DayCount, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading the rollup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DayCount
	for rows.Next() {
		var c DayCount
		if err := rows.Scan(&c.Day, &c.Name, &c.Project, &c.Count, &c.Installs); err != nil {
			return nil, fmt.Errorf("reading the rollup: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sortDayCounts puts the rollup in a stable order — newest day first, then
// name, then project — so a client can diff two /v1/stats responses.
func sortDayCounts(cs []DayCount) {
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Day != b.Day {
			return a.Day > b.Day
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Project < b.Project
	})
}
