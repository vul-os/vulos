-- CLUSTER-02: durable write-through schema for auth.Store.
-- All objects use IF NOT EXISTS so migration is idempotent.

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id   TEXT PRIMARY KEY,
    data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    data       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS profiles (
    user_id TEXT PRIMARY KEY,
    data    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);

-- VUMAIL-06: encrypted recovery-kit blobs (one per user).
CREATE TABLE IF NOT EXISTS recovery_blobs (
    user_id TEXT PRIMARY KEY,
    blob    BLOB NOT NULL
);
