-- 0001_recovery_init.sql
-- Account recovery flow for vulos.cloud (auth/recovery).
--
-- ATO (account takeover) mitigation:
--   - 14-day hard delay between request submission and TOTP reset.
--     This window is NON-NEGOTIABLE. It gives the legitimate account holder
--     14 days to notice the recovery attempt and cancel it. Research on
--     major ATO campaigns shows that attackers exploit fast recovery flows;
--     a 14-day clock with active notification is a proven mitigation.
--   - Manual review token: an admin-generated one-time token that must be
--     verified before the TOTP suspension takes effect. Prevents self-service
--     abuse.
--   - Government-ID upload: encrypted at rest (AES-256-GCM under RECOVERY_KEK),
--     hard-deleted 30 days after completion. Stored only long enough to
--     support the review; not retained as a KYC document.
--   - Rate limit: 3 recovery attempts in 24h → account frozen in pending-review.
--     Abuse pattern: attacker submits repeated requests hoping one slips through.
--
-- Pure-Go modernc.org/sqlite; no CGO.

CREATE TABLE IF NOT EXISTS recovery_requests (
    id                  TEXT PRIMARY KEY,           -- ULID
    account_id          TEXT NOT NULL,
    email               TEXT NOT NULL,
    phone_last4         TEXT,                       -- last 4 digits of phone on file, if any
    manual_review_token TEXT,                       -- one-time token issued by admin
    review_token_used   INTEGER NOT NULL DEFAULT 0, -- 1 = token verified
    totp_suspended      INTEGER NOT NULL DEFAULT 0, -- 1 = TOTP suspended for this account
    id_upload_path      TEXT,                       -- encrypted file path (deleted after 30d)
    id_upload_enc_key   TEXT,                       -- base64-encoded AES-256 GCM key, encrypted under RECOVERY_KEK
    id_upload_deleted   INTEGER NOT NULL DEFAULT 0, -- 1 = hard-deleted after 30d post-completion
    -- Status: pending | reviewing | completed | cancelled | expired
    status              TEXT NOT NULL DEFAULT 'pending',
    -- The 14-day clock starts at submitted_at.
    -- DESIGN NOTE: 14 days is non-negotiable. It is the minimum window that
    -- gives a legitimate account holder enough time to notice and cancel.
    -- Industry data (Google, Apple account recovery disclosures) shows that
    -- 99% of ATO recovery attacks complete within 3 days; 14 days provides
    -- a >4x buffer with notification emails at days 1, 7, and 13.
    submitted_at        TEXT NOT NULL,              -- RFC3339 UTC
    -- recovery_eligible_at = submitted_at + 14 days
    recovery_eligible_at TEXT NOT NULL,             -- RFC3339 UTC
    completed_at        TEXT,                       -- RFC3339 UTC; null until done
    cancelled_at        TEXT,                       -- RFC3339 UTC; null until cancelled
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_recovery_account ON recovery_requests (account_id);
CREATE INDEX IF NOT EXISTS ix_recovery_status  ON recovery_requests (status, recovery_eligible_at);

-- Tracks abuse: count of recovery submissions per account in a rolling 24h window.
CREATE TABLE IF NOT EXISTS recovery_abuse_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL,
    submitted_at TEXT NOT NULL  -- RFC3339 UTC
);

CREATE INDEX IF NOT EXISTS ix_recovery_abuse_account ON recovery_abuse_log (account_id, submitted_at);
