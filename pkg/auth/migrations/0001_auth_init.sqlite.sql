-- 0001_auth_init.sqlite.sql
-- Auth tables for vulos.cloud (SQLite), folded initial schema.
--
-- This file is the single, folded initial migration for the auth subsystem.
-- It supersedes the former chain 0001..0015; every table/column/index below is
-- declared in its FINAL shape (no ALTER/DROP). Notes on the fold:
--   * users:   locked_until, email_verified_grace_until, preferred_2fa_modality,
--              suspended, home_region and recovery_email were added over the
--              life of the chain (old 0003/0005/0007/0008/0010/0014). They are
--              declared inline in append order so the stored schema text is
--              byte-identical to the historical ADD COLUMN result.
--   * sessions: partial (old 0002) is likewise folded inline in append order.
--   * oauth_identities (old 0009) was DROPPED by old 0015 — consumer social
--     sign-in is gone (Vulos is an identity PROVIDER, not a client). The
--     create-then-drop nets to nothing, so the table is simply absent here.
--
-- Auth decision (LOCKED): email + password only for native accounts. No
-- oauth_google_sub column anywhere in this schema.

CREATE TABLE IF NOT EXISTS users (
    id               TEXT PRIMARY KEY,              -- ULID (26-char Crockford base32)
    email            TEXT UNIQUE NOT NULL,
    password_hash    TEXT NOT NULL,                 -- argon2id-encoded; always present, no IdP path
    email_verified   INTEGER NOT NULL DEFAULT 0,    -- 0=unverified, 1=verified
    totp_secret_enc  TEXT,                          -- AES-GCM(STORAGE_KEK) encrypted TOTP secret; nullable until 2FA enrolled
    totp_enabled     INTEGER NOT NULL DEFAULT 0,
    fleet_admin      INTEGER NOT NULL DEFAULT 0,    -- true -> mandatory 2FA + 14+ char min
    failed_2fa_count INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL,                 -- RFC3339 UTC
    updated_at       TEXT NOT NULL                  -- RFC3339 UTC
, locked_until INTEGER, email_verified_grace_until INTEGER, preferred_2fa_modality TEXT NOT NULL DEFAULT 'password+totp', suspended INTEGER NOT NULL DEFAULT 0, home_region TEXT NOT NULL DEFAULT 'eu', recovery_email TEXT);

CREATE INDEX IF NOT EXISTS ix_users_email ON users (email);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT PRIMARY KEY,                        -- 32-byte crypto/rand token, base64url-encoded
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,                           -- RFC3339 UTC
    last_seen_at TEXT NOT NULL,                           -- RFC3339 UTC
    expires_at   TEXT NOT NULL,                           -- RFC3339 UTC
    ip           TEXT,                                    -- remote IP at creation time
    user_agent   TEXT,                                    -- User-Agent at creation time
    device_key   TEXT,                                    -- stable per-device fingerprint hash
    revoked      INTEGER NOT NULL DEFAULT 0               -- 1=revoked
, partial INTEGER NOT NULL DEFAULT 0);

CREATE INDEX IF NOT EXISTS ix_sessions_user ON sessions (user_id);

CREATE TABLE IF NOT EXISTS password_resets (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    created_at TEXT NOT NULL,  -- RFC3339 UTC
    expires_at TEXT NOT NULL,  -- RFC3339 UTC
    used       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_password_resets_user ON password_resets (user_id);

CREATE TABLE IF NOT EXISTS totp_recovery_codes (
    id         TEXT PRIMARY KEY,              -- ULID
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,                 -- argon2id hash of a single-use recovery code
    used       INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_totp_recovery_user ON totp_recovery_codes (user_id);

CREATE TABLE IF NOT EXISTS login_attempts (
    id    INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    ip    TEXT NOT NULL,
    ok    INTEGER NOT NULL,                  -- 1=success, 0=failure
    at    TEXT NOT NULL                      -- RFC3339 UTC
);

CREATE INDEX IF NOT EXISTS ix_login_attempts_email ON login_attempts (email, at);
CREATE INDEX IF NOT EXISTS ix_login_attempts_ip ON login_attempts (ip, at);

CREATE TABLE IF NOT EXISTS pending_totp_secrets (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret     TEXT NOT NULL,        -- plaintext base32 secret; only held briefly until /totp/confirm
    rec_codes  TEXT,                 -- JSON array of plaintext recovery codes shown on /enroll
    created_at TEXT NOT NULL         -- RFC3339 UTC; expire stale entries server-side
);

CREATE TABLE IF NOT EXISTS device_tokens (
    id          TEXT PRIMARY KEY,          -- ULID
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL,             -- SHA-256 hex of the full vc_device cookie value
    fp_hash     TEXT NOT NULL,             -- SHA-256 hex of the device fingerprint (ip+ua+user_id)
    created_at  TEXT NOT NULL,             -- RFC3339 UTC
    expires_at  TEXT NOT NULL,             -- RFC3339 UTC
    rotated     INTEGER NOT NULL DEFAULT 0 -- 1 = superseded (keep for replay detection)
);

CREATE INDEX IF NOT EXISTS ix_device_tokens_user ON device_tokens (user_id);
CREATE INDEX IF NOT EXISTS ix_device_tokens_hash ON device_tokens (token_hash);

CREATE TABLE IF NOT EXISTS email_verification_tokens (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,  -- RFC3339 UTC
    expires_at TEXT NOT NULL,  -- RFC3339 UTC
    used       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_evtokens_user ON email_verification_tokens (user_id);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id            TEXT    PRIMARY KEY,
    user_id       TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id TEXT    NOT NULL UNIQUE,
    public_key    BLOB    NOT NULL,
    sign_count    INTEGER NOT NULL DEFAULT 0,
    transports    TEXT    NOT NULL DEFAULT '[]',
    friendly_name TEXT    NOT NULL DEFAULT '',
    aaguid        TEXT    NOT NULL DEFAULT '',
    last_used_at  TEXT,
    created_at    TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_webauthn_creds_user ON webauthn_credentials (user_id);
CREATE INDEX IF NOT EXISTS ix_webauthn_creds_cred_id ON webauthn_credentials (credential_id);

CREATE TABLE IF NOT EXISTS legal_dpa_acceptances (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id  TEXT    NOT NULL,
    version     TEXT    NOT NULL DEFAULT '1.0',
    accepted_at TEXT    NOT NULL,
    ip          TEXT,
    user_agent  TEXT,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_legal_dpa_account ON legal_dpa_acceptances(account_id);

CREATE TABLE IF NOT EXISTS webauthn_challenges (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    session_data TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    created_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_webauthn_challenges_user ON webauthn_challenges (user_id);
CREATE INDEX IF NOT EXISTS ix_webauthn_challenges_expires ON webauthn_challenges (expires_at);

CREATE TABLE IF NOT EXISTS opaque_records (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    envelope   BLOB NOT NULL,
    suite      TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS opaque_logins (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expected_mac   BLOB NOT NULL,
    session_secret BLOB NOT NULL,
    expires_at     TEXT NOT NULL,
    created_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_opaque_logins_expires ON opaque_logins (expires_at);
