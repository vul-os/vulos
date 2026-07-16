-- 0001_integrations_init.sqlite.sql — third-party OAuth integration tokens,
-- per-device public-key registry, and connected EXTERNAL mailboxes (SQLite).
--
-- Folded initial migration (was 0001 integrations_init + 0002 device_keys +
-- 0003 mail_accounts + 0004 mail_caldav). Purely additive: the caldav_url /
-- carddav_url columns (formerly ALTER TABLE mail_accounts ADD COLUMN in 0004)
-- are now declared inline.
--
-- INTEG-01. The control plane is the OAuth broker for the fleet: it holds the
-- single registered client_id/secret and a stable redirect_uri, runs the
-- authorization-code exchange, and custodies the long-lived refresh_token
-- ENCRYPTED at rest. Individual Vulos OS boxes never see the refresh_token or
-- the client secret — they mint short-lived access tokens over the existing
-- authenticated CP↔box channel (see routes_integrations.go).
--
-- This is a CONNECTED-ACCOUNT integration (data access: Gmail/Drive/Calendar +
-- browser session seeding), NOT an identity provider. AUTH-02 removed Google as
-- a *login* provider and that decision stands — there is deliberately no
-- google_sub login path here; the google_sub column below is display-only
-- (to label which Google account is connected).

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

-- Lookup by account for the connected-accounts UI.
CREATE INDEX IF NOT EXISTS idx_integration_account ON integration_connections(account_id);

-- Per-device public-key registry (INTEG-SEC-01). Binds a device ULID to the
-- public half of its on-box identity key (devicekey.KeyStore — TPM or AES-GCM
-- software key, ECDSA P-256). The token mint verifies a per-device signature
-- against this pinned key, so a box that merely knows the fleet-wide
-- DEVICE_SHARED_SECRET + a victim's ULID can no longer mint that victim's tokens
-- — it must hold the device private key.
--
-- Trust-on-first-use: the FIRST registration for a ULID wins and is pinned. A
-- later registration presenting a DIFFERENT key for the same ULID is rejected
-- (tamper signal) — see DeviceKeyStore.Pin / ErrDeviceKeyConflict.
CREATE TABLE IF NOT EXISTS device_pubkeys (
    ulid        TEXT NOT NULL PRIMARY KEY,     -- canonical device ULID
    pubkey_der  BLOB NOT NULL,                 -- PKIX DER of the device public key
    algo        TEXT NOT NULL DEFAULT 'ecdsa-p256',
    created_at  INTEGER NOT NULL               -- unix seconds (pin time)
);

-- Connected EXTERNAL mailboxes (MAIL-CONNECT-01). A Cloud account can connect
-- EXTERNAL mailboxes — Gmail, Microsoft/Outlook (OAuth2) or a generic IMAP/SMTP
-- server — so the Vulos Workspace mail UI drives them through the lilmail engine.
-- The CP is the broker: it custodies the refresh_token / IMAP password ENCRYPTED
-- at rest and mints short-lived per-mailbox credentials for lilmail.
--
-- caldav_url / carddav_url (folded from 0004): for a generic IMAP mailbox the
-- operator may supply EXPLICIT CalDAV/CardDAV base URLs at connect time (some
-- IMAP hosts run a co-located DAV server); these are persisted here. Empty => the
-- proxy omits the brokered X-Vulos-Mail-Caldav-Url / X-Vulos-Mail-Carddav-Url
-- headers and lilmail falls back to its own discovery. For gmail the URLs are
-- DERIVED from the address at broker time and are NOT stored.
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
    updated_at        INTEGER NOT NULL              -- unix seconds
, caldav_url  TEXT NOT NULL DEFAULT '', carddav_url TEXT NOT NULL DEFAULT '');

-- Lookup all mailboxes for an account (connected-accounts UI / proxy resolution).
CREATE INDEX IF NOT EXISTS idx_mail_accounts_account ON mail_accounts(account_id);

-- One row per (account, provider, mailbox address): re-connecting the same
-- mailbox replaces it rather than duplicating.
CREATE UNIQUE INDEX IF NOT EXISTS idx_mail_accounts_unique
    ON mail_accounts(account_id, provider, email);
