-- MINST-01: instance registry — local manifest of all account instances.
-- All objects use IF NOT EXISTS so migration is idempotent.

CREATE TABLE IF NOT EXISTS instances (
    ulid             TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL DEFAULT 'device'
                         CHECK (kind IN ('device', 'cloud')),
    endpoint_url     TEXT NOT NULL DEFAULT '',
    ed25519_public_key TEXT NOT NULL DEFAULT '',
    role             TEXT NOT NULL DEFAULT 'peer'
                         CHECK (role IN ('owner', 'peer')),
    status           TEXT NOT NULL DEFAULT 'unknown'
                         CHECK (status IN ('online', 'offline', 'unknown')),
    last_seen_at     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_instances_status ON instances(status);
