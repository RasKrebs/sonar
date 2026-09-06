package sessions

import (
	_ "embed"

	"github.com/raskrebs/sonar/internal/store"
)

// Migration 005 is the version contract §8 reserves for sessions. It is
// registered from here, not from internal/store, because that is what a
// reserved version means: the store holds the slot open and the owning package
// fills it in from its own init(). internal/store asserts that its own
// embedded set never takes a reserved number.
//
//go:embed migrations/005_sessions.sql
var migration005 string

func init() {
	store.RegisterMigration(store.VersionSessions, "sessions", migration005)
}
