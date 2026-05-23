-- MAIL-AI: per-account opt-in flag and token usage tracking.
-- All statements use IF NOT EXISTS / OR IGNORE so migration is idempotent.

-- accounts table: stores per-account AI mail opt-in and monthly usage.
CREATE TABLE IF NOT EXISTS accounts (
    account_id           TEXT    NOT NULL PRIMARY KEY,
    ai_mail_enabled      INTEGER NOT NULL DEFAULT 0,  -- BOOLEAN: 0=disabled, 1=enabled
    monthly_token_usage  INTEGER NOT NULL DEFAULT 0,
    updated_at           TEXT    NOT NULL DEFAULT ''
);

-- ai_mail_audit: records enable/disable toggle events for audit trail.
CREATE TABLE IF NOT EXISTS ai_mail_audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id  TEXT    NOT NULL,
    action      TEXT    NOT NULL,  -- 'enable' | 'disable'
    created_at  TEXT    NOT NULL DEFAULT ''
);

-- ai_mail_token_log: per-call token usage log (content NOT stored).
CREATE TABLE IF NOT EXISTS ai_mail_token_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id  TEXT    NOT NULL,
    endpoint    TEXT    NOT NULL,  -- 'smart-compose' | 'summarize' | 'reply-suggestions' | 'extract-actions'
    model_slug  TEXT    NOT NULL,
    token_count INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL DEFAULT ''
);
