# The sonar relay

The relay is the server side of sonar: one small HTTP service, run by us for
the hosted app and published as `ghcr.io/raskrebs/sonar-relay` so anyone can
run their own.

Today it does one thing — collect anonymous product telemetry and answer
aggregate questions about it. It is the same service that will later terminate
exposed tunnels and hold sign-in, which is why it is a service and not a
serverless function: the tables and the auth are meant to grow.

It has nothing to do with the local daemon. `sonar serve` watches your ports
over a unix socket and speaks the daemon protocol; `sonar relay serve` answers
HTTP for a fleet and does not know the protocol exists. They share a binary so
there is one artefact to build, sign and release, and nothing else.

## Running it

```sh
sonar relay serve
```

That binds `:8787`, keeps its data in `./relay.db` and accepts unauthenticated
batches — fine on a laptop, not fine on the internet. Every flag has an
environment equivalent, because the relay's home is a container:

| Flag | Environment | Default | What it does |
| --- | --- | --- | --- |
| `--listen` | `SONAR_RELAY_LISTEN` | `:8787` | Address to bind |
| `--db` | `SONAR_RELAY_DB` | `./relay.db` | SQLite file |
| `--database-url` | `DATABASE_URL` | — | Postgres URL; replaces SQLite when set |
| `--project-keys` | `SONAR_RELAY_PROJECT_KEYS` | — | Comma-separated keys that authorise `/v1` |
| `--retention-days` | `SONAR_RELAY_RETENTION_DAYS` | `90` | Days of raw events to keep |
| `--log-level` | — | `info` | `debug`, `info`, `warn`, `error` |

It listens on **all interfaces** by default. That is deliberate — inside a
container `127.0.0.1` is unreachable — and it means the relay must never be
exposed directly. Put a TLS-terminating proxy in front of it; there is one
below.

Logs are JSON on stderr, one object per line, which is what every log shipper
wants and what the startup line makes parseable:

```json
{"time":"2026-09-06T20:33:28Z","level":"INFO","msg":"relay listening","addr":"[::]:8787","auth":true,"retention_days":90}
```

`SIGINT` and `SIGTERM` shut it down gracefully: in-flight requests finish, the
listener closes, the rollup loop stops.

## Deploying it

One command, on a fresh Hetzner box with Docker installed:

```sh
mkdir sonar-relay && cd sonar-relay \
  && base=https://raw.githubusercontent.com/raskrebs/sonar/main/docs/relay \
  && curl -fsSLO "$base/docker-compose.yml" && curl -fsSLO "$base/Caddyfile" \
  && printf 'RELAY_DOMAIN=relay.example.com\nRELAY_KEY=%s\n' "$(openssl rand -hex 24)" > .env \
  && docker compose up -d && cat .env
```

That brings up the relay and a Caddy in front of it, and Caddy fetches a
Let's Encrypt certificate for `RELAY_DOMAIN` on its own — there is no
certificate step. Point `RELAY_DOMAIN` at the box first, and edit `.env` before
`docker compose up` if you would rather not use the example name. The last
`cat` prints the generated key: it is what the desktop client and `/v1/stats`
present, and it is only stored in that file.

The compose file is [`docs/relay/docker-compose.yml`](relay/docker-compose.yml)
and the Caddyfile is [`docs/relay/Caddyfile`](relay/Caddyfile). The relay is
never published on a host port; only Caddy is.

### On Fly

```sh
fly launch --no-deploy --image ghcr.io/raskrebs/sonar-relay:latest --name my-sonar-relay
fly volumes create relay_data --size 1
fly secrets set SONAR_RELAY_PROJECT_KEYS="$(openssl rand -hex 24)"
fly deploy
```

with this in `fly.toml`:

```toml
[env]
  SONAR_RELAY_LISTEN = ":8787"
  SONAR_RELAY_DB     = "/data/relay.db"

[[mounts]]
  source      = "relay_data"
  destination = "/data"

[http_service]
  internal_port       = 8787
  force_https         = true
  auto_stop_machines  = false
  min_machines_running = 1

  [[http_service.checks]]
    path     = "/healthz"
    interval = "15s"
    timeout  = "2s"
```

Fly terminates TLS, so no Caddy. `auto_stop_machines = false` matters: the
hourly rollup is a goroutine in this process, and a machine that sleeps does
not run it.

For Postgres instead of SQLite, drop the volume and set `DATABASE_URL`. The
schema is created on first connect either way.

### Health checks

`GET /healthz` is unauthenticated, cheap and does not touch the database — it
answers "is this process alive", which is the question an orchestrator is
asking. The image is distroless and has no shell, so there is no `HEALTHCHECK`
in it; point the platform's probe at `/healthz`.

## The HTTP surface

Three routes. Anything else is a 404, and a wrong method on a known route is a
405.

### `POST /v1/events`

Accepts a batch. Headers: `X-Sonar-Project` (optional, defaults to `default`)
and, when the relay has keys, `X-Sonar-Key` or `Authorization: Bearer`.

```json
{
  "install_id": "8f14e45f-ceea-467a-9c1b-2f0e1d3a4b5c",
  "app_version": "v0.6.0",
  "daemon_version": "v0.6.0",
  "os": "darwin",
  "arch": "arm64",
  "sent_at": "2026-09-06T09:12:04Z",
  "events": [
    {"name": "view", "at": "2026-09-06T09:12:00Z", "props": {"name": "ports"}},
    {"name": "action", "props": {"name": "kill", "host_kind": "local", "outcome": "ok"}}
  ]
}
```

`sent_at` is accepted and ignored. The relay stamps its own `received_at` and
trusts nothing a client says about when it spoke; the field is in the contract
so the desktop can send it, and dropping it is the correct handling.

`202` with `{"accepted": 2}`. Every 4xx is `{"error": "<code>", "reason": "…"}`,
where the reason names the field and the rule it broke:

```json
{"error": "unsafe_prop", "reason": "events[0].props.root looks like a filesystem path and may not be sent"}
```

| Status | When |
| --- | --- |
| `202` | Stored |
| `400` | Any validation rule below |
| `401` | Keys are configured and none was presented |
| `413` | Body over 256 KB |
| `429` | More than 1 batch/s (burst 10) from this `install_id` |
| `500` | The database refused the write |

### `GET /v1/stats`

Query: `days` (1–365, default 30) and `project`. Needs a key when keys are
configured.

```json
{
  "generated_at": "2026-09-06T20:33:28Z",
  "days": 30,
  "installs": {"active_1d": 41, "active_7d": 180, "active_30d": 412, "total": 903},
  "events": [
    {"day": "2026-09-06", "name": "app_open", "project": "default", "count": 128, "installs": 41}
  ]
}
```

`installs` counts distinct installs whose last batch falls in each window.
`events` is one row per (day, name, project), newest day first. Days that still
have raw events are computed live, so the numbers do not go stale between
hourly rollups; older days come from `daily_rollup`.

### `GET /healthz`

`200` with `{"ok": true}`. Never authenticated.

## What may be sent

The contract is `internal/relay/contract.go` and nowhere else. The desktop
client implements the same rules so a value the relay would refuse never
leaves the machine.

- **`install_id`** — a canonical lowercase UUID, `8-4-4-4-12`. No braces, no
  `urn:uuid:` prefix, no uppercase: one spelling means the primary key cannot
  alias itself.
- **`os` / `arch`** — a known `GOOS` / `GOARCH` spelling, or absent.
- **`app_version` / `daemon_version`** — at most 64 characters of
  `[A-Za-z0-9._+-]`.
- **`events`** — at most 500 per batch. An empty list is a legal no-op.
- **`name`** — one of the closed set in `EventNames`. An unknown name is a
  `400`, not a dropped row: it means the client and the relay disagree, and
  that should be loud. Adding a name is a relay release and then a client
  release, in that order; names are never removed, because an old install
  keeps sending them.
- **`at`** — RFC 3339, optional (the relay stamps its own `received_at`
  regardless). More than 24 hours in the future is a `400` rather than a clamp:
  a wrong clock is worth surfacing, and clamping would poison the rollups.
- **`props`** — a flat object, at most 20 keys.
  - Keys are 1–40 characters of `^[a-z][a-z0-9_]*$`.
  - Values are strings, finite numbers, booleans or null. An object or an
    array is a `400` — props are flat, and nesting is how payloads grow into
    somewhere to hide a path.
  - String values are at most 64 characters.
  - A string value is refused if it contains `://`, contains `/` or `\`,
    starts with `~`, looks like `C:\…`, contains `@`, or reads as a dotted
    hostname (a final label of two or more letters, so `v0.6.0` is fine and
    `sonar.local` is not).

That last rule is blunt on purpose. Telemetry carries counts and short
enum-ish labels; it never carries anything a path, a host or a URL could be
hiding in, so the relay refuses the *shapes* rather than trying to judge each
value.

Unknown fields are ignored at both levels, so a newer client keeps working
against an older relay. That is the point of the relay having its own release
train.

### Event names

Two halves. The **generic** five are what the desktop emits: a whole product
fits in them because the specificity lives in props, so shipping a new view or
a new action never needs a relay release. The **specific** names are for the
CLI and daemon side, where there is no view or action to name. New desktop
instrumentation belongs in the generic five; reach for a specific name only
when neither framing fits.

Props are a suggestion the client is expected to keep — the relay enforces the
*shape* rules above, not this column. Nothing here may carry a hostname, a
path, a process name, a command line or a port; counts and enums only.

**Generic**

| Name | Props | Emitted when |
| --- | --- | --- |
| `onboarding_step` | `step`, `outcome` (`completed`/`skipped`/`failed`) | An onboarding step ends |
| `view` | `name` | A view is opened |
| `action` | `name`, `host_kind` (`local`/`remote`), `outcome` | A user action completes |
| `settings_change` | `key` — never the value | A setting is changed |
| `interest` | `surface` | An `IsBuilding` surface is clicked |

**Specific**

| Name | Props | Emitted when |
| --- | --- | --- |
| `app_open` / `app_close` | — | The app starts or exits |
| `app_update` | `from`, `to` | The app replaced itself |
| `daemon_start` / `daemon_stop` | — | The daemon's lifecycle |
| `scan` | `ports`, `ms` | A port scan finished |
| `port_killed` | `signal`, `outcome` | A listener was killed |
| `service_start` / `service_stop` | `outcome` | One service in a group |
| `group_up` / `group_down` | `services`, `outcome` | A group was brought up or down |
| `rename` / `assign` | — | A port was renamed or grouped |
| `map_created` | — | A port map was added |
| `remote_add` / `remote_install` | `outcome` | A remote host was registered or installed onto |
| `host_switch` | `host_kind` | The host switcher moved |
| `mcp_tool_call` | `tool`, `outcome` | An agent called an MCP tool |
| `hook_fired` | `hook` | A Claude Code hook ran |
| `skill_installed` | `scope` | `sonar install skills` ran |
| `update_check` / `update_applied` | `outcome` | The CLI self-update path |
| `expose_start` / `expose_stop` | `provider`, `outcome` | Milestone 4, not emitted yet |
| `sign_in` | `outcome` | Milestone 4, not emitted yet |
| `error` | `where`, `code` | Anything failed in a way worth counting |

`internal/relay/contract.go` holds the list; this table is the prose copy. A
name that is not on it is a `400`.

## Storage

SQLite by default (`modernc.org/sqlite`, WAL, pure Go so `CGO_ENABLED=0`
holds), Postgres when `DATABASE_URL` is set. One set of SQL serves both: times
are fixed-width UTC RFC 3339 text, so `substr(at, 1, 10)` is the UTC day and
every window is a string range.

```
installs      install_id, first_seen, last_seen, app_version, daemon_version,
              os, arch, project
events        id, install_id, name, at, received_at, project, props (json text)
daily_rollup  day, name, project, count, installs   -- primary key (day, name, project)
```

An hourly goroutine runs a rollup and then a retention delete, in that order,
and runs one pass at startup so a restart is also a repair:

- the **rollup** recomputes `daily_rollup` from the raw events that are still
  on disk. It is a recompute, not an increment, which makes it safe to run
  twice, safe to run after a crash, and safe to run on a tick;
- **retention** then deletes raw events older than `--retention-days`. Rollups
  are never deleted — the aggregate is the thing worth keeping, and it is
  already anonymous.

So a day whose raw rows have expired keeps the totals the last pass wrote. That
is the whole reason `daily_rollup` exists.

## Authentication

With no `--project-keys`, the relay is open. That is only safe on a private
network, and the startup log says `"auth":false` so it is visible.

With keys configured, `/v1/events` and `/v1/stats` need one in `X-Sonar-Key` or
`Authorization: Bearer`. Keys are compared in constant time, and every
configured key is compared on every request, so neither the number of keys nor
the position of the match is timeable. `/healthz` is always open.

A key is a shared secret, not an identity: it says "this deployment accepts
your batches". Real sign-in arrives with the expose milestone.

## Rate limiting

One batch per second per `install_id`, bursting to ten, as a token bucket.
Keying on the install rather than the IP is what stops one noisy client from
throttling a whole NAT'd office. Over the limit is `429` with `Retry-After`.

Buckets are swept after 15 minutes of silence and capped at 100 000 keys —
`install_id` is client-chosen, so an unbounded map would be a memory leak with
a trigger. At the cap the limiter fails open rather than locking honest
installs out.

The limit is applied *after* validation, so a body that cannot name a valid
install can never spend someone else's tokens.

## Building the image

```sh
docker build -f Dockerfile.relay -t sonar-relay .
docker run -p 8787:8787 -v sonar-relay:/data sonar-relay
```

`.github/workflows/release.yml` pushes
`ghcr.io/raskrebs/sonar-relay:<tag>` and `:latest` for `linux/amd64` and
`linux/arm64` on every `v*` tag. Both architectures are native `go build`s
cross-compiled from the runner's own — there is no QEMU in that job.

The image runs as uid 65532. A **named** volume inherits that owner; a bind
mount has to be `chown 65532:65532`'d first.

## Tests

```sh
go test ./internal/relay/...                              # handlers, storage, rollup
go test -tags integration ./internal/relay/...            # the real binary on a random port
TEST_DATABASE_URL=postgres://… go test ./internal/relay/... # the same storage tests on Postgres
```

The Postgres tests are skipped unless `TEST_DATABASE_URL` names a throwaway
database; they delete every row in the relay's three tables before they run.
