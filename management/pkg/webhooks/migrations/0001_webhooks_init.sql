-- 0001_webhooks_init.sql
-- Webhook delivery system for vulos.cloud.
-- Per-subscription queue table in SQLite; pure-Go modernc.org/sqlite; no CGO.

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id            TEXT PRIMARY KEY,     -- ULID
    account_id    TEXT NOT NULL,
    url           TEXT NOT NULL,
    secret        TEXT NOT NULL,        -- HMAC-SHA256 secret (raw bytes, base64url)
    topics        TEXT NOT NULL,        -- JSON array of topic strings
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    active        INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS ix_wh_subs_account ON webhook_subscriptions (account_id);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              TEXT PRIMARY KEY,   -- ULID
    subscription_id TEXT NOT NULL REFERENCES webhook_subscriptions(id),
    topic           TEXT NOT NULL,
    payload         TEXT NOT NULL,      -- JSON payload
    status          TEXT NOT NULL DEFAULT 'pending', -- pending|delivered|failed|dead
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,      -- RFC3339 UTC
    last_attempt_at TEXT,               -- RFC3339 UTC; null = never attempted
    last_status_code INTEGER,           -- HTTP status code of last attempt
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_wh_deliveries_status ON webhook_deliveries (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS ix_wh_deliveries_sub ON webhook_deliveries (subscription_id);
