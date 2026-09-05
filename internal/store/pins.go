package store

import (
	"errors"

	"github.com/raskrebs/sonar/internal/state"
)

// Group pins are manual group assignments (`sonar assign 3000 sonar`). They
// win over every other group source — precedence manual > start > file > auto
// — and are keyed by the same match keys as renames.

// GetPin returns the group pinned to key.
func (s *Store) GetPin(key string) (string, bool, error) {
	return s.getKeyed("group_pins", "group_name", key)
}

// SetPin pins key to group, replacing any earlier pin for it.
func (s *Store) SetPin(key, group string) error {
	if key == "" {
		return errors.New("store: pin needs a match key")
	}
	if err := validName(group); err != nil {
		return err
	}
	return s.exec(
		`INSERT INTO group_pins(key, group_name, created_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET group_name = excluded.group_name, created_at = excluded.created_at`,
		key, group, nowString(),
	)
}

// ClearPin removes the pin for key. Clearing an absent key is not an error.
func (s *Store) ClearPin(key string) error {
	return s.exec(`DELETE FROM group_pins WHERE key = ?`, key)
}

// Pins returns every stored pin as key→group.
func (s *Store) Pins() (map[string]string, error) {
	return s.allKeyed(`SELECT key, group_name FROM group_pins`)
}

// ResolvePin finds the pin that applies to p, trying every match key in order
// of specificity.
func (s *Store) ResolvePin(p state.Port) (string, bool, error) {
	return s.resolve("group_pins", "group_name", p)
}
