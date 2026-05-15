-- Vulos SQLite schema with cr-sqlite CRDT support.
-- This file is embedded into the binary and applied by the migration runner.
-- All tables are registered as CRRs (conflict-free replicated relations) so
-- cr-sqlite can merge changes from multiple nodes automatically.
--
-- Migration IDs are recorded in the _migrations table.
-- Every statement is idempotent: CREATE TABLE IF NOT EXISTS, etc.

-- ── Migration bookkeeping ────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS _migrations (
    id         TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);

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
