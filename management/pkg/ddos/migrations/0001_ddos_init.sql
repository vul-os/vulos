CREATE TABLE IF NOT EXISTS blocklist (
    cidr           TEXT PRIMARY KEY,
    reason         TEXT NOT NULL DEFAULT '',
    ttl_expires_at TEXT,
    added_by       TEXT NOT NULL DEFAULT 'system',
    added_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ddos_settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
