-- 0001_apikeys_init.sql
-- Unified API-key store for vulos.cloud/cp (APIKEYS-01).
-- Pure-Go modernc.org/sqlite; no CGO.
--
-- Keys have the form:  vk_<43-char base64url>
-- Only the SHA-256 hex digest (key_hash) is persisted — the raw secret is
-- returned exactly once at creation and is never logged.
--
-- scopes / products are stored as JSON arrays:
--   scopes   e.g. ["documents.read","mail.read"]
--   products e.g. ["office","mail"]
--
-- Revocation: revoked_at non-NULL means the key is invalid.
-- Expiry:     expires_at non-NULL means the key expires at that timestamp (RFC3339 UTC).

CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,               -- Crockford-base32 ULID (26 chars)
    account_id  TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',       -- user-facing label
    key_hash    TEXT NOT NULL UNIQUE,           -- SHA-256 hex of the raw vk_ key
    scopes      TEXT NOT NULL DEFAULT '[]',    -- JSON array of scope strings
    products    TEXT NOT NULL DEFAULT '[]',    -- JSON array of product strings
    created_at  TEXT NOT NULL,                 -- RFC3339 UTC
    revoked_at  TEXT,                          -- RFC3339 UTC; NULL = active
    expires_at  TEXT                           -- RFC3339 UTC; NULL = never expires
);

CREATE INDEX IF NOT EXISTS ix_api_keys_account    ON api_keys (account_id);
CREATE INDEX IF NOT EXISTS ix_api_keys_key_hash   ON api_keys (key_hash);
