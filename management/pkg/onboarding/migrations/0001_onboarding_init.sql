-- SUPPORT-05: onboarding state tracking + day-7 migration gate.
-- Pure-Go modernc.org/sqlite; no CGO.
-- Idempotent: CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS.

-- One row per account. Created on first POST /api/onboarding/step.
-- account_created_at drives the 7-day migration gate.
CREATE TABLE IF NOT EXISTS onboarding_state (
    account_id          TEXT    PRIMARY KEY,
    account_created_at  TEXT    NOT NULL,   -- RFC3339; set once at creation
    domain_connected    INTEGER NOT NULL DEFAULT 0,
    dns_verified        INTEGER NOT NULL DEFAULT 0,
    first_mail_sent     INTEGER NOT NULL DEFAULT 0,
    first_backup_done   INTEGER NOT NULL DEFAULT 0,
    first_device_paired INTEGER NOT NULL DEFAULT 0,
    first_user_invited  INTEGER NOT NULL DEFAULT 0,
    updated_at          TEXT    NOT NULL    -- RFC3339
);

CREATE INDEX IF NOT EXISTS idx_onboarding_account
    ON onboarding_state(account_id);
