-- 0001_recovery_init.postgres.sql
-- Postgres variant: AUTOINCREMENT→BIGSERIAL.
CREATE TABLE IF NOT EXISTS recovery_requests (
    id                  TEXT PRIMARY KEY,
    account_id          TEXT NOT NULL,
    email               TEXT NOT NULL,
    phone_last4         TEXT,
    manual_review_token TEXT,
    review_token_used   INTEGER NOT NULL DEFAULT 0,
    totp_suspended      INTEGER NOT NULL DEFAULT 0,
    id_upload_path      TEXT,
    id_upload_enc_key   TEXT,
    id_upload_deleted   INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'pending',
    submitted_at        TEXT NOT NULL,
    recovery_eligible_at TEXT NOT NULL,
    completed_at        TEXT,
    cancelled_at        TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_recovery_account ON recovery_requests (account_id);
CREATE INDEX IF NOT EXISTS ix_recovery_status  ON recovery_requests (status, recovery_eligible_at);

CREATE TABLE IF NOT EXISTS recovery_abuse_log (
    id           BIGSERIAL PRIMARY KEY,
    account_id   TEXT NOT NULL,
    submitted_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_recovery_abuse_account ON recovery_abuse_log (account_id, submitted_at);
