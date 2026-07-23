-- SUPPORT-03: three-tier support ticket store.
-- Pure-Go modernc.org/sqlite; no CGO.
-- Idempotent: CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS support_tickets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id  TEXT    NOT NULL,
    tier        TEXT    NOT NULL,           -- billing tier at time of opening
    priority    TEXT    NOT NULL DEFAULT 'P3',  -- P1 | P2 | P3
    channel     TEXT    NOT NULL,           -- 'email' | 'dedicated_slack'
    subject     TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    state       TEXT    NOT NULL DEFAULT 'open',  -- 'open' | 'closed'
    breach_at   TEXT    NOT NULL,           -- RFC3339 SLA deadline
    breached    INTEGER NOT NULL DEFAULT 0, -- 1 = SLA breached
    opened_at   TEXT    NOT NULL,           -- RFC3339
    resolved_at TEXT                        -- RFC3339 or NULL
);

CREATE INDEX IF NOT EXISTS idx_support_tickets_account
    ON support_tickets(account_id, opened_at DESC);

CREATE INDEX IF NOT EXISTS idx_support_tickets_open_breach
    ON support_tickets(state, breach_at)
    WHERE state = 'open';
