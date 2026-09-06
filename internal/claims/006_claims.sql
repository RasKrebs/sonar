-- 006_claims.sql — port claims (spec 2 §4).
--
-- A claim is a reservation in sonar's book-keeping, not an OS-level bind: it
-- keeps parallel agents off each other's ports and gives one (project,
-- worktree) pair the same ports every time. One row per claimed port; the key
-- is `<project>/<worktree>` and groups the ports a caller holds.

CREATE TABLE IF NOT EXISTS claims (
    port       INTEGER PRIMARY KEY,
    key        TEXT NOT NULL,
    project    TEXT NOT NULL,
    worktree   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_claims_key     ON claims(key);
CREATE INDEX IF NOT EXISTS idx_claims_expires ON claims(expires_at);
