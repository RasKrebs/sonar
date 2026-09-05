package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Event kinds written to the history ring. They are the port-scoped subset of
// the state.event kinds the daemon publishes.
const (
	EventPortUp        = "port_up"
	EventPortDown      = "port_down"
	EventPortRestarted = "port_restarted"
)

// HistoryEvent is one row of the port_events ring. The first six fields are
// the wire shape of ports.history; Bind, ProjectRoot and Command are extra
// columns the daemon spec's table carries and are omitted from JSON when
// empty.
type HistoryEvent struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	Port        int       `json:"port"`
	PID         int       `json:"pid"`
	DisplayName string    `json:"display_name"`
	Group       string    `json:"group"`

	Bind        string `json:"bind,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Command     string `json:"command,omitempty"`
}

// DefaultHistoryLimit is what Query uses when limit <= 0, matching
// `sonar history` with no arguments.
const DefaultHistoryLimit = 50

// HistoryCapacity is the number of rows the ring keeps. Enforced in SQL by
// the port_events_ring trigger in migration 001; the constant is here so
// tests and callers can talk about it.
const HistoryCapacity = 10000

// Append records one port event. The daemon calls it on the scanner goroutine
// after publishing a delta, never on the RPC path.
func (s *Store) Append(e HistoryEvent) error {
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("store: history event needs a kind")
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	return s.exec(
		`INSERT INTO port_events
		   (at, kind, port, bind, pid, display_name, group_name, project_root, command)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timeToString(at), e.Kind, e.Port, e.Bind, e.PID, e.DisplayName, e.Group,
		e.ProjectRoot, e.Command,
	)
}

// AppendBatch records several events in one transaction. A scan tick that
// sees a whole group come up writes one transaction instead of N.
func (s *Store) AppendBatch(events []HistoryEvent) error {
	if len(events) == 0 {
		return nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(
		`INSERT INTO port_events
		   (at, kind, port, bind, pid, display_name, group_name, project_root, command)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, e := range events {
		if strings.TrimSpace(e.Kind) == "" {
			return errors.New("store: history event needs a kind")
		}
		at := e.At
		if at.IsZero() {
			at = time.Now()
		}
		if _, err := stmt.Exec(timeToString(at), e.Kind, e.Port, e.Bind, e.PID,
			e.DisplayName, e.Group, e.ProjectRoot, e.Command); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Query returns events newest first. port filters to one port when non-nil,
// since drops anything older when non-zero, and limit <= 0 means
// DefaultHistoryLimit.
func (s *Store) Query(port *int, since time.Time, limit int) ([]HistoryEvent, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	var (
		where []string
		args  []any
	)
	if port != nil {
		where = append(where, "port = ?")
		args = append(args, *port)
	}
	if !since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, timeToString(since))
	}
	q := `SELECT at, kind, port, bind, pid, display_name, group_name, project_root, command
	      FROM port_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("reading port_events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]HistoryEvent, 0, min(limit, 128))
	for rows.Next() {
		var (
			e  HistoryEvent
			at string
		)
		if err := rows.Scan(&at, &e.Kind, &e.Port, &e.Bind, &e.PID, &e.DisplayName,
			&e.Group, &e.ProjectRoot, &e.Command); err != nil {
			return nil, fmt.Errorf("reading port_events: %w", err)
		}
		if e.At, err = timeFromString(at); err != nil {
			return nil, fmt.Errorf("reading port_events: bad timestamp %q: %w", at, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HistoryCount is the number of rows currently in the ring.
func (s *Store) HistoryCount() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM port_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting port_events: %w", err)
	}
	return n, nil
}
