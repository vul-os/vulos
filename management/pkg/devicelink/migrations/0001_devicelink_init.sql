-- 0001_devicelink_init.sql
-- Device-code account LINK flow for headless/self-host installs
-- (WAVE24-RELAY-BILLING). Idempotent: safe to run against an existing DB.
--
-- device_links tracks a pending → approved → consumed device-authorization
-- grant. The device_code is stored ONLY as a sha256 hash (raw value returned to
-- the install exactly once, at start); the user_code is the short human-typed
-- approval code (looked up by an authenticated human).
--
-- install_credentials is the account-bound bearer minted on the approving poll,
-- also stored only as a hash. Presenting the raw token resolves to account_id.
CREATE TABLE IF NOT EXISTS device_links (
    device_code_hash TEXT NOT NULL PRIMARY KEY,  -- sha256(device_code)
    user_code        TEXT NOT NULL UNIQUE,       -- short human approval code (normalized)
    account_id       TEXT NOT NULL DEFAULT '',   -- bound on approve
    state            TEXT NOT NULL,               -- pending|approved|denied|consumed
    created_at       TEXT NOT NULL,               -- RFC3339 UTC
    expires_at       TEXT NOT NULL,               -- RFC3339 UTC
    approved_at      TEXT,
    consumed_at      TEXT
);

CREATE INDEX IF NOT EXISTS ix_device_links_expires ON device_links(expires_at);

CREATE TABLE IF NOT EXISTS install_credentials (
    token_hash  TEXT NOT NULL PRIMARY KEY,  -- sha256(install credential)
    account_id  TEXT NOT NULL,
    created_at  TEXT NOT NULL,              -- RFC3339 UTC
    revoked     INTEGER NOT NULL DEFAULT 0  -- 1 = revoked (soft-delete)
);

CREATE INDEX IF NOT EXISTS ix_install_credentials_account ON install_credentials(account_id);
