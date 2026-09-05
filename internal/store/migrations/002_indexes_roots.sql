-- 002_indexes_roots.sql — history query indexes and the known-roots table.

CREATE INDEX IF NOT EXISTS idx_port_events_at      ON port_events(at DESC);
CREATE INDEX IF NOT EXISTS idx_port_events_port_at ON port_events(port, at DESC);

-- Directories where a .sonar.yaml has been seen. The groups resolver keeps
-- its in-memory index warm from this table across daemon restarts.
CREATE TABLE IF NOT EXISTS known_roots (
    path     TEXT PRIMARY KEY,
    added_at TEXT NOT NULL
);
