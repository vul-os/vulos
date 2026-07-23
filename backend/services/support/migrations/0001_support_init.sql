-- Help & Support: box-owner support requests store.
-- Pure-Go modernc.org/sqlite; no CGO. All DDL is idempotent.

CREATE TABLE IF NOT EXISTS support_requests (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id  TEXT    NOT NULL,
    tier        TEXT    NOT NULL,               -- plan tier at time of submission
    priority    TEXT    NOT NULL DEFAULT 'P3',   -- P1 | P2 | P3
    channel     TEXT    NOT NULL,                -- 'email' | 'priority'
    subject     TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    state       TEXT    NOT NULL DEFAULT 'open', -- 'open' | 'closed'
    breach_at   INTEGER NOT NULL,                -- unix seconds, SLA target
    opened_at   INTEGER NOT NULL,                -- unix seconds
    resolved_at INTEGER                          -- unix seconds or NULL
);

CREATE INDEX IF NOT EXISTS idx_support_requests_account
    ON support_requests(account_id, opened_at DESC);
