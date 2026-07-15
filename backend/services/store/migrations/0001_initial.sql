-- Vulos SQLite schema with cr-sqlite CRDT support — clean baseline.
-- This file is embedded into the binary and applied by the shared forward-only
-- migration runner (internal/migrate); applied migrations are recorded in the
-- schema_migrations bookkeeping table that the runner owns. All tables are
-- registered as CRRs (conflict-free replicated relations) after migration so
-- cr-sqlite can merge changes from multiple nodes automatically.
-- Every statement is idempotent: CREATE TABLE IF NOT EXISTS, etc.

-- ── Auth: users ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT,
    email         TEXT,
    name          TEXT,
    picture       TEXT,
    providers     TEXT,   -- JSON map of provider → access-token/sub
    created_at    TEXT,
    last_login    TEXT
);

-- ── Auth: sessions ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    email      TEXT,
    name       TEXT,
    device_id  TEXT,
    expires_at TEXT,
    created_at TEXT
);

-- ── Auth: profiles ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS profiles (
    user_id      TEXT PRIMARY KEY,
    display_name TEXT,
    role         TEXT DEFAULT 'user',
    theme        TEXT DEFAULT 'dark',
    avatar       TEXT,
    timezone     TEXT,
    preferences  TEXT   -- JSON map
);

-- ── Settings ─────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT,
    updated_at TEXT
);

-- ── App install list (synced intent; not local install state) ─────────────────
CREATE TABLE IF NOT EXISTS installed_apps (
    app_id       TEXT PRIMARY KEY,
    version      TEXT NOT NULL,
    installed_by TEXT,           -- node ID that first installed it
    installed_at TEXT,
    status       TEXT DEFAULT 'active'  -- 'active', 'removed', 'pending'
);

-- ── Local-only app install progress (NOT synced via cr-sqlite) ───────────────
CREATE TABLE IF NOT EXISTS local_app_status (
    app_id     TEXT PRIMARY KEY,
    state      TEXT,    -- 'installing', 'installed', 'failed', 'removing'
    progress   TEXT,    -- human-readable progress string
    error      TEXT,
    updated_at TEXT
);
