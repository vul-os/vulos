-- 0001_superadmin_init.postgres.sql
-- Super-admin tables for vulos.cloud control plane (Postgres variant).
-- Idempotent: CREATE TABLE IF NOT EXISTS throughout.

-- superadmins: tracks which accounts have super-admin rights.
CREATE TABLE IF NOT EXISTS superadmins (
    account_id   TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    promoted_at  TEXT NOT NULL,                           -- RFC3339 UTC
    promoted_by  TEXT,                                    -- account_id of promoter; NULL for bootstrap
    revoked_at   TEXT                                     -- NULL = active
);

-- admin_sessions: short-lived post-WebAuthn admin sessions.
CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash   TEXT PRIMARY KEY,                        -- SHA-256 hex of raw token
    account_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ip           TEXT NOT NULL,
    ua           TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,                           -- RFC3339 UTC
    last_seen_at TEXT NOT NULL,                           -- RFC3339 UTC
    expires_at   TEXT NOT NULL                            -- RFC3339 UTC; 30-min idle
);

CREATE INDEX IF NOT EXISTS ix_admin_sessions_account ON admin_sessions (account_id);
CREATE INDEX IF NOT EXISTS ix_admin_sessions_expires ON admin_sessions (expires_at);

-- admin_webauthn_credentials: hardware keys for super-admins.
CREATE TABLE IF NOT EXISTS admin_webauthn_credentials (
    credential_id    TEXT PRIMARY KEY,                    -- base64url-encoded credential ID
    account_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key       BYTEA NOT NULL,
    sign_count       BIGINT NOT NULL DEFAULT 0,
    transports       TEXT NOT NULL DEFAULT '[]',          -- JSON array of strings
    attestation_type TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL                        -- RFC3339 UTC
);

CREATE INDEX IF NOT EXISTS ix_admin_wa_creds_account ON admin_webauthn_credentials (account_id);

-- additional_reserved_handles: DB-backed reserved handles (beyond hard-coded set).
CREATE TABLE IF NOT EXISTS additional_reserved_handles (
    handle    TEXT PRIMARY KEY,
    reason    TEXT NOT NULL DEFAULT '',
    added_by  TEXT REFERENCES users(id) ON DELETE SET NULL,
    added_at  TEXT NOT NULL                               -- RFC3339 UTC
);

-- pending_admin_webauthn: server-side challenge storage for admin WebAuthn ceremonies.
CREATE TABLE IF NOT EXISTS pending_admin_webauthn (
    account_id   TEXT PRIMARY KEY,
    session_json TEXT NOT NULL,
    created_at   TEXT NOT NULL
);
