-- 0001_publicapi_init.sql
-- Public REST API token store for vulos.cloud.
-- Pure-Go modernc.org/sqlite; no CGO.

CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,      -- ULID
    account_id   TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,  -- SHA-256 hex of the raw bearer token
    label        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,         -- RFC3339 UTC
    revoked      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_api_tokens_account ON api_tokens (account_id);
