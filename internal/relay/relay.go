// Package relay is the sonar relay: the small HTTP service that receives
// anonymous product telemetry from sonar installs and answers aggregate
// questions about it.
//
// It is deliberately separate from the daemon. The daemon protocol
// (docs/schema/protocol.schema.json) does not know the relay exists, nothing in
// internal/daemon imports this package, and the relay never opens a sonar
// database: `sonar relay serve` is a second, unrelated server that happens to
// ship in the same binary so there is exactly one artefact to build, sign and
// release.
//
// The relay runs in a container. It listens on all interfaces by default,
// keeps its state in SQLite (modernc, pure Go, so CGO_ENABLED=0 holds) or in
// Postgres when DATABASE_URL is set, and expects a TLS-terminating proxy in
// front of it — docs/RELAY.md has a Caddy example and a one-command deploy.
//
// The wire contract lives in this package and nowhere else: [Batch] is the
// body of POST /v1/events, [EventNames] is the closed set of names the relay
// accepts, and [ValidateBatch] is the single implementation of every rule.
// The desktop client mirrors these rules; when one changes, it changes here
// first.
//
// Milestone 4 grows this service into the expose/auth backend. Nothing here
// should assume telemetry is all it will ever serve, which is why the router,
// the auth check and the storage interface are separate from the handlers.
package relay
