-- 0001_integrations_init.postgres.sql — third-party OAuth integration tokens,
-- per-device public-key registry, and connected EXTERNAL mailboxes (Postgres).
--
-- Folded initial migration (was 0001 integrations_init + 0002 device_keys +
-- 0003 mail_accounts + 0004 mail_caldav). Purely additive: the caldav_url /
-- carddav_url columns (formerly ALTER TABLE mail_accounts ADD COLUMN in 0004)
-- are now declared inline. Postgres variant uses BYTEA for the raw DER device
-- public key. See the .sqlite.sql variant for the full rationale.

CREATE TABLE IF NOT EXISTS integration_connections (
    account_id        TEXT NOT NULL,                 -- auth.users.id (ULID)
    provider          TEXT NOT NULL,                 -- "google"
    refresh_token_enc TEXT NOT NULL,                 -- AES-256-GCM, base64 (nonce||ct||tag)
    access_token_enc  TEXT NOT NULL DEFAULT '',      -- cached short-lived access token (encrypted)
    access_expiry     INTEGER NOT NULL DEFAULT 0,    -- unix seconds; 0 = no cached access token
    scopes            TEXT NOT NULL DEFAULT '',      -- space-separated granted scopes
    account_email     TEXT NOT NULL DEFAULT '',      -- connected account's email (display only)
    account_sub       TEXT NOT NULL DEFAULT '',      -- provider subject id (display only, NOT login)
    created_at        INTEGER NOT NULL,              -- unix seconds
    updated_at        INTEGER NOT NULL,              -- unix seconds
    PRIMARY KEY (account_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_integration_account ON integration_connections(account_id);

-- Per-device public-key registry (INTEG-SEC-01). BYTEA holds the PKIX DER of the
-- device public key. Trust-on-first-use pinning; see the SQLite variant.
CREATE TABLE IF NOT EXISTS device_pubkeys (
    ulid        TEXT NOT NULL PRIMARY KEY,     -- canonical device ULID
    pubkey_der  BYTEA NOT NULL,                -- PKIX DER of the device public key
    algo        TEXT NOT NULL DEFAULT 'ecdsa-p256',
    created_at  BIGINT NOT NULL               -- unix seconds (pin time)
);

-- Connected EXTERNAL mailboxes (MAIL-CONNECT-01). caldav_url / carddav_url folded
-- inline from 0004. See the SQLite variant for the full rationale.
CREATE TABLE IF NOT EXISTS mail_accounts (
    id                TEXT PRIMARY KEY,             -- random id (mailbox handle used in /api/mail URLs)
    account_id        TEXT NOT NULL,                -- auth.users.id (owner, ULID)
    provider          TEXT NOT NULL,                -- "gmail" | "outlook" | "imap"
    email             TEXT NOT NULL DEFAULT '',     -- mailbox address (display + IMAP/SASL username)

    -- OAuth providers (gmail / outlook). Empty for imap.
    refresh_token_enc TEXT NOT NULL DEFAULT '',     -- AES-256-GCM, base64 (nonce||ct||tag)
    access_token_enc  TEXT NOT NULL DEFAULT '',     -- cached short-lived access token (encrypted)
    access_expiry     INTEGER NOT NULL DEFAULT 0,   -- unix seconds; 0 = no cached access token
    scopes            TEXT NOT NULL DEFAULT '',     -- space-separated granted scopes

    -- Generic IMAP/SMTP (imap). Empty for OAuth providers.
    imap_host         TEXT NOT NULL DEFAULT '',
    imap_port         INTEGER NOT NULL DEFAULT 0,
    smtp_host         TEXT NOT NULL DEFAULT '',
    smtp_port         INTEGER NOT NULL DEFAULT 0,
    secret_enc        TEXT NOT NULL DEFAULT '',     -- IMAP/SMTP password, AES-256-GCM base64

    status            TEXT NOT NULL DEFAULT 'active', -- active | error
    created_at        INTEGER NOT NULL,             -- unix seconds
    updated_at        INTEGER NOT NULL,             -- unix seconds
    caldav_url        TEXT NOT NULL DEFAULT '',
    carddav_url       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_mail_accounts_account ON mail_accounts(account_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mail_accounts_unique
    ON mail_accounts(account_id, provider, email);
