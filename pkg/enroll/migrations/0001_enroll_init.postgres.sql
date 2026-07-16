-- 0001_enroll_init.postgres.sql
-- Postgres variant: BLOB→BYTEA, DATETIME→TEXT, remove SQLite STRFTIME default.
CREATE TABLE IF NOT EXISTS enroll_grants (
    device_code   TEXT        NOT NULL PRIMARY KEY,
    user_code     TEXT        NOT NULL UNIQUE,
    state         TEXT        NOT NULL DEFAULT 'pending',
    account_id    TEXT,
    device_pubkey BYTEA       NOT NULL,
    device_cert   BYTEA,
    ulid          TEXT,
    expires_at    TEXT        NOT NULL,
    approved_at   TEXT,
    created_at    TEXT        NOT NULL DEFAULT (TO_CHAR(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
);

CREATE INDEX IF NOT EXISTS idx_enroll_grants_user_code ON enroll_grants (user_code);
CREATE INDEX IF NOT EXISTS idx_enroll_grants_expires_at ON enroll_grants (expires_at);
