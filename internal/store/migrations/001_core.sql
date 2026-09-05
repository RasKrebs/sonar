-- 001_core.sql — core tables for renames, group pins and port history.
--
-- Owned by internal/store. Forward-only: never edit a shipped migration,
-- add a new one.

CREATE TABLE IF NOT EXISTS renames (
    key        TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS group_pins (
    key        TEXT PRIMARY KEY,
    group_name TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS port_events (
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

-- Ring buffer: keep only the newest 10 000 rows. Ids come from AUTOINCREMENT
-- and are strictly increasing, so "id <= NEW.id - 10000" is exactly the rows
-- that fell out of the window.
CREATE TRIGGER IF NOT EXISTS port_events_ring
AFTER INSERT ON port_events
BEGIN
    DELETE FROM port_events WHERE id <= NEW.id - 10000;
END;
