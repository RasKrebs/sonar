-- Frozen copy of the schema a sonar with migrations 001 and 002 wrote, used to
-- prove that migration 005 lands on a real user's database without losing
-- anything. Do not "fix" this file when 001 or 002 change: the point is that
-- it is what is on old users' disks.
CREATE TABLE schema_version (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE renames (
    key        TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE group_pins (
    key        TEXT PRIMARY KEY,
    group_name TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE port_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    at           TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    port         INTEGER NOT NULL,
    bind         TEXT    NOT NULL DEFAULT '',
    pid          INTEGER NOT NULL DEFAULT 0,
    display_name TEXT    NOT NULL DEFAULT '',
    group_name   TEXT    NOT NULL DEFAULT '',
    project_root TEXT    NOT NULL DEFAULT '',
    command      TEXT    NOT NULL DEFAULT ''
);

CREATE TRIGGER port_events_ring
AFTER INSERT ON port_events
BEGIN
    DELETE FROM port_events WHERE id <= NEW.id - 10000;
END;

INSERT INTO schema_version(version, name, applied_at)
VALUES (1, 'core', '2026-01-01T00:00:00.000000000Z');

INSERT INTO renames(key, name, created_at)
VALUES ('cwd:/home/me/code/shop:3000', 'storefront', '2026-01-01T00:00:00.000000000Z');

INSERT INTO group_pins(key, group_name, created_at)
VALUES ('port:9999', 'legacy', '2026-01-01T00:00:00.000000000Z');

INSERT INTO port_events(at, kind, port, pid, display_name, group_name)
VALUES ('2026-01-01T00:00:00.000000000Z', 'port_up', 3000, 42, 'storefront', 'shop');

-- Migration 002, as shipped: history indexes and the known-roots table.
CREATE INDEX idx_port_events_at      ON port_events(at DESC);
CREATE INDEX idx_port_events_port_at ON port_events(port, at DESC);

CREATE TABLE known_roots (
    path     TEXT PRIMARY KEY,
    added_at TEXT NOT NULL
);

INSERT INTO schema_version(version, name, applied_at)
VALUES (2, 'indexes_roots', '2026-01-02T00:00:00.000000000Z');

INSERT INTO known_roots(path, added_at)
VALUES ('/home/me/code/shop', '2026-01-02T00:00:00.000000000Z');
