-- 0001_oauthprovider_init.sql — Vulos as an OAuth2 / OIDC PROVIDER.
--
-- OAUTHP-01. Vulos Cloud controls identity for the suite, so it issues
-- "Sign in with Vulos" + scoped API access. The Vulos Mail product and approved
-- third parties run the Authorization Code + PKCE flow against Vulos accounts.
--
-- Identity reuse: the subject of every token is an auth.users.id (ULID). We do
-- NOT build a parallel user store here — internal/auth remains the single
-- source of truth for who a user is. These tables only hold the *delegation*
-- artefacts (registered clients, one-time codes, issued tokens) and the RS256
-- signing keypair.
--
-- Security invariants encoded below:
--   - client secrets are stored HASHED (sha256 hex), never in plaintext;
--   - public clients carry no secret and MUST use PKCE (is_public=1);
--   - auth codes are stored HASHED, single-use (consumed flag) and short-TTL;
--   - access/refresh tokens are stored HASHED with explicit expiry + revoke;
--   - redirect URIs are matched EXACTLY (newline-separated allow-list).
--
-- Folded initial migration. The following formerly-separate, purely-additive
-- migrations are declared inline here (their ADD COLUMNs are folded into the
-- owning CREATE, in the exact append form the prior SQLite ALTERs produced, so
-- a fresh :memory: schema dump is byte-identical to running the full chain):
--   0002_oauthprovider_consents  → oauth_consents table + index
--   0003_token_family            → oauth_tokens.family_id + idx_oauth_tokens_family
--   0004_signing_key_rotation    → oauth_signing_keys.retire_at
--   0005_pairwise_subject        → oauth_clients.subject_type + .sector_identifier,
--                                  oauth_provider_secrets table

CREATE TABLE IF NOT EXISTS oauth_clients (
    client_id      TEXT PRIMARY KEY,             -- public, random ("vcid_…")
    secret_hash    TEXT NOT NULL DEFAULT '',     -- hex(sha256(secret)); '' for public clients
    is_public      INTEGER NOT NULL DEFAULT 0,   -- 1 = public client (no secret, PKCE only)
    name           TEXT NOT NULL,                -- human label shown on the consent screen
    redirect_uris  TEXT NOT NULL DEFAULT '',     -- newline-separated, exact-match allow-list
    scopes         TEXT NOT NULL DEFAULT '',     -- space-separated scopes this client may request
    owner_user_id  TEXT NOT NULL,                -- auth.users.id of the registering developer
    created_at     BIGINT NOT NULL,              -- unix seconds
    updated_at     BIGINT NOT NULL               -- unix seconds
, subject_type TEXT NOT NULL DEFAULT 'public', sector_identifier TEXT NOT NULL DEFAULT '');

CREATE INDEX IF NOT EXISTS idx_oauth_clients_owner ON oauth_clients(owner_user_id);

-- One-time authorization codes. Stored hashed; single-use via `consumed`.
CREATE TABLE IF NOT EXISTS oauth_authcodes (
    code_hash             TEXT PRIMARY KEY,       -- hex(sha256(raw code))
    client_id             TEXT NOT NULL,
    user_id               TEXT NOT NULL,          -- auth.users.id (the consenting subject)
    redirect_uri          TEXT NOT NULL,          -- must match again at token exchange
    scope                 TEXT NOT NULL,          -- space-separated granted scopes
    code_challenge        TEXT NOT NULL,          -- PKCE challenge (S256, base64url)
    code_challenge_method TEXT NOT NULL,          -- always "S256"
    nonce                 TEXT NOT NULL DEFAULT '', -- OIDC nonce echoed into the id_token
    expires_at            BIGINT NOT NULL,        -- unix seconds
    consumed              INTEGER NOT NULL DEFAULT 0,
    created_at            BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oauth_authcodes_expires ON oauth_authcodes(expires_at);

-- Issued access + refresh tokens. Opaque random strings, stored hashed.
-- family_id (RFC 9700) ties a refresh-token rotation chain together so the whole
-- family can be revoked as a unit; NULL only for legacy pre-fold tokens.
CREATE TABLE IF NOT EXISTS oauth_tokens (
    token_hash   TEXT PRIMARY KEY,                -- hex(sha256(raw token))
    token_type   TEXT NOT NULL,                   -- "access" | "refresh"
    client_id    TEXT NOT NULL,
    user_id      TEXT NOT NULL,                   -- auth.users.id
    scope        TEXT NOT NULL,                   -- space-separated granted scopes
    expires_at   BIGINT NOT NULL,                 -- unix seconds
    revoked      INTEGER NOT NULL DEFAULT 0,
    created_at   BIGINT NOT NULL
, family_id TEXT);

CREATE INDEX IF NOT EXISTS idx_oauth_tokens_user   ON oauth_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_client ON oauth_tokens(client_id);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_family ON oauth_tokens(family_id);

-- RS256 signing keypair(s) for id_token + JWKS. Generated + persisted on first
-- boot so tokens survive restarts. PEM-encoded; the active key signs, all
-- non-revoked keys are published in JWKS so in-flight tokens still verify.
-- retire_at (unix seconds, NULLABLE) is the rotation overlap window: NULL = live
-- / never retired (always in JWKS); >now = retired but still published during
-- rollover; <=now = fully expired (dropped from JWKS).
CREATE TABLE IF NOT EXISTS oauth_signing_keys (
    kid          TEXT PRIMARY KEY,                -- key id (random)
    private_pem  TEXT NOT NULL,                   -- PKCS#8 PEM (RSA private key)
    public_pem   TEXT NOT NULL,                   -- PKIX PEM (RSA public key)
    active       INTEGER NOT NULL DEFAULT 1,      -- 1 = current signing key
    created_at   BIGINT NOT NULL
, retire_at BIGINT);

-- Stored consent grants (OAUTHP-01): one row per (user_id, client_id); a wider
-- approval UPSERTs the row rather than duplicating; revoked=1 rows are kept for
-- audit and live checks require revoked=0.
CREATE TABLE IF NOT EXISTS oauth_consents (
    user_id     TEXT NOT NULL,
    client_id   TEXT NOT NULL,
    scope       TEXT NOT NULL DEFAULT '',  -- space-separated granted scopes
    granted_at  BIGINT NOT NULL,           -- unix seconds
    revoked     INTEGER NOT NULL DEFAULT 0,
    revoked_at  BIGINT,                    -- unix seconds, NULL when not revoked
    PRIMARY KEY (user_id, client_id)
);

CREATE INDEX IF NOT EXISTS idx_oauth_consents_user ON oauth_consents(user_id);

-- Server-held secrets for the provider (currently: the pairwise-sub salt).
-- Kept out of the signing-keys table so it has its own lifecycle.
CREATE TABLE IF NOT EXISTS oauth_provider_secrets (
    name       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at BIGINT NOT NULL
);
