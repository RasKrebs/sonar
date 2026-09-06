-- 005_sessions.sql — agent sessions (spec 2 §3, contract §8 reserved version 5).
--
-- One row per agent session sonar has ever attributed a run to. Live sessions
-- are held in memory by the daemon's run registry; this table is what survives
-- a restart and what keeps an inactive session readable for the seven days
-- `port_history` may still name it. Rows are pruned on last_seen.

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    tool       TEXT    NOT NULL DEFAULT '',
    label      TEXT    NOT NULL DEFAULT '',
    worktree   TEXT    NOT NULL DEFAULT '',
    branch     TEXT    NOT NULL DEFAULT '',
    detected   INTEGER NOT NULL DEFAULT 0,
    first_seen TEXT    NOT NULL,
    last_seen  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_last_seen ON sessions(last_seen DESC);
