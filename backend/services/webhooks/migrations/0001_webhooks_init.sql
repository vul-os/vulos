-- 0001_webhooks_init.sql
-- Outbound webhook subscriptions for the box owner (Settings -> Developer ->
-- Webhooks). Single-owner scope: one box, one set of subscriptions, gated to
-- the admin role at the route layer (see routes_webhooks.go). Pure-Go SQLite
-- (modernc.org/sqlite), no CGO.

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id            TEXT PRIMARY KEY,      -- random 26-char Crockford id
    url           TEXT NOT NULL,
    secret        TEXT NOT NULL,         -- HMAC-SHA256 secret, base64url-encoded
    topics        TEXT NOT NULL,         -- JSON array of topic strings
    active        INTEGER NOT NULL DEFAULT 1,
    created_by    TEXT NOT NULL DEFAULT '', -- user id of the admin who created it (audit only)
    created_at    INTEGER NOT NULL,      -- unix seconds UTC
    updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id                TEXT PRIMARY KEY,  -- random 26-char Crockford id
    subscription_id   TEXT NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    topic             TEXT NOT NULL,
    payload           TEXT NOT NULL,     -- JSON payload
    status            TEXT NOT NULL DEFAULT 'pending', -- pending|delivered|failed|dead
    attempt_count     INTEGER NOT NULL DEFAULT 0,
    next_attempt_at   INTEGER NOT NULL,  -- unix seconds UTC
    last_attempt_at   INTEGER,           -- unix seconds UTC; null = never attempted
    last_status_code  INTEGER,           -- HTTP status code of the last attempt
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_wh_deliveries_status ON webhook_deliveries (status, next_attempt_at);
CREATE INDEX IF NOT EXISTS ix_wh_deliveries_sub ON webhook_deliveries (subscription_id, created_at DESC);
