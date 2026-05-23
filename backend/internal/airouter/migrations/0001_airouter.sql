-- AIROT-01: AI Router config + provider tables.
-- All statements use IF NOT EXISTS so migration is idempotent.

CREATE TABLE IF NOT EXISTS airouter_config (
    id          INTEGER PRIMARY KEY CHECK (id = 1), -- singleton row
    mode        TEXT    NOT NULL DEFAULT 'byo',     -- 'byo' | 'cloud'
    active_model TEXT   NOT NULL DEFAULT '',
    updated_at  TEXT    NOT NULL DEFAULT ''
);

-- Ensure the singleton row always exists.
INSERT OR IGNORE INTO airouter_config(id, mode, active_model, updated_at)
VALUES(1, 'byo', '', '');

CREATE TABLE IF NOT EXISTS airouter_providers (
    id           TEXT PRIMARY KEY,   -- UUID / ulid
    display_name TEXT NOT NULL,
    base_url     TEXT NOT NULL,
    api_key_enc  TEXT NOT NULL DEFAULT '', -- AES-GCM encrypted, hex-encoded
    models       TEXT NOT NULL DEFAULT '', -- comma-separated model slugs
    created_at   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT ''
);
