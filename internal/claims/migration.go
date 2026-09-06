package claims

import (
	_ "embed"

	"github.com/raskrebs/sonar/internal/store"
)

// Migration 006 is the version the cross-spec contract (§8) reserves for
// claims. Like the other reserved slots it is registered from the package that
// owns it rather than shipped inside internal/store, so a build that never
// links claims never grows the table.
//
// It is applied by any store the claims package is linked into — the daemon,
// and the CLI through it. A database that reaches version 6 before migration
// 005 (sessions) exists in the binary would never see 005 afterwards, because
// store.migrate compares against the highest applied version; both migrations
// ship in the same binary from the first release that has either, so the gap
// only matters to a development build of one branch alone.
//
//go:embed 006_claims.sql
var migrationSQL string

func init() {
	store.RegisterMigration(store.VersionClaims, "claims", migrationSQL)
}
