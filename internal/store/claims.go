package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// A claim is a reservation in sonar's book-keeping: the port is not bound, it
// is only spoken for, so `ports.next` and the next `claims.acquire` step over
// it. Migration 006 holds one row per claimed port; the key groups the ports
// one (project, worktree) pair holds.

// ErrClaimed is returned by Claims.Put when a port is already held by a
// different key. The daemon maps it to the contract's claim_conflict (1201).
var ErrClaimed = errors.New("port is claimed by another key")

// ClaimRow is one claimed port.
type ClaimRow struct {
	Port      int
	Key       string
	Project   string
	Worktree  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Claims is the claims table. Get it from Store.Claims; the zero value is not
// usable.
type Claims struct{ s *Store }

// Claims returns the claims table view of the store.
func (s *Store) Claims() Claims { return Claims{s: s} }

// Put writes rows, reusing the stored created_at of a port this key already
// holds so a refresh does not restart its clock. The whole set is written in
// one transaction: either every port is claimed or none is, which is what lets
// two acquires racing for the same block fail cleanly instead of interleaving.
//
// A port held by a different, unexpired key is never taken: Put returns
// ErrClaimed and rolls back. Sweep expired rows first (see Expire) so a stale
// row does not block a live caller.
func (c Claims) Put(rows ...ClaimRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("store: claim port %d is out of range", r.Port)
		}
		if strings.TrimSpace(r.Key) == "" {
			return errors.New("store: a claim needs a key")
		}
	}

	c.s.wmu.Lock()
	defer c.s.wmu.Unlock()

	tx, err := c.s.db.Begin()
	if err != nil {
		return fmt.Errorf("claiming ports: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range rows {
		var holder string
		var createdAt string
		err := tx.QueryRow(`SELECT key, created_at FROM claims WHERE port = ?`, r.Port).
			Scan(&holder, &createdAt)
		switch {
		case err == nil && holder != r.Key:
			return fmt.Errorf("port %d: %w (%s)", r.Port, ErrClaimed, holder)
		case err == nil:
			// Keep the original created_at; only the expiry moves.
		default:
			createdAt = timeToString(nonZero(r.CreatedAt))
		}
		if _, err := tx.Exec(
			`INSERT INTO claims(port, key, project, worktree, created_at, expires_at)
			 VALUES(?, ?, ?, ?, ?, ?)
			 ON CONFLICT(port) DO UPDATE SET
			     key        = excluded.key,
			     project    = excluded.project,
			     worktree   = excluded.worktree,
			     expires_at = excluded.expires_at`,
			r.Port, r.Key, r.Project, r.Worktree, createdAt, timeToString(nonZero(r.ExpiresAt)),
		); err != nil {
			return fmt.Errorf("claiming port %d: %w", r.Port, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("claiming ports: %w", err)
	}
	return nil
}

// Get returns the ports held under key, lowest first.
func (c Claims) Get(key string) ([]ClaimRow, error) {
	return c.query(`SELECT port, key, project, worktree, created_at, expires_at
	                  FROM claims WHERE key = ? ORDER BY port`, key)
}

// List returns every claim row, by key then port.
func (c Claims) List() ([]ClaimRow, error) {
	return c.query(`SELECT port, key, project, worktree, created_at, expires_at
	                  FROM claims ORDER BY key, port`)
}

// Delete drops every port held under key and reports how many rows went.
// Releasing a key nobody holds is not an error.
func (c Claims) Delete(key string) (int, error) {
	return c.deleteWhere(`DELETE FROM claims WHERE key = ?`, key)
}

// Expire drops every claim that expired at or before now and reports how many
// rows went. Every claims.* call sweeps first, which is the lazy pruning the
// spec asks for.
func (c Claims) Expire(now time.Time) (int, error) {
	return c.deleteWhere(`DELETE FROM claims WHERE expires_at <= ?`, timeToString(nonZero(now)))
}

func (c Claims) deleteWhere(query string, args ...any) (int, error) {
	c.s.wmu.Lock()
	defer c.s.wmu.Unlock()
	res, err := c.s.db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("removing claims: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // the write happened; the driver just cannot count it
	}
	return int(n), nil
}

func (c Claims) query(query string, args ...any) ([]ClaimRow, error) {
	rows, err := c.s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ClaimRow
	for rows.Next() {
		var r ClaimRow
		var created, expires string
		if err := rows.Scan(&r.Port, &r.Key, &r.Project, &r.Worktree, &created, &expires); err != nil {
			return nil, fmt.Errorf("reading claims: %w", err)
		}
		if r.CreatedAt, err = timeFromString(created); err != nil {
			return nil, fmt.Errorf("claim on port %d has an unreadable created_at: %w", r.Port, err)
		}
		if r.ExpiresAt, err = timeFromString(expires); err != nil {
			return nil, fmt.Errorf("claim on port %d has an unreadable expires_at: %w", r.Port, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nonZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
