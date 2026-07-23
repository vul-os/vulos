-- Abuse detection + T&S queue state (RISK-ABUSE-01).
--
-- Three logical concerns share one DB to keep wiring trivial:
--
--   1. suspended_accounts   — auto-suspend ledger (one row per account)
--   2. abuse_reports        — human T&S intake queue (drained by reviewers)
--   3. abuse_signals        — recent detector signals (debug + post-hoc forensics)
--
-- The sliding-window rate counters live in-memory inside the Detector; they are
-- intentionally NOT persisted (lost on restart). The persistent state below is
-- the durable, human-actionable side: who got suspended, what reports came in.
--
-- Pure-Go modernc.org/sqlite only — no CGO.

CREATE TABLE IF NOT EXISTS suspended_accounts (
    account_id    TEXT PRIMARY KEY,
    suspended_at  TEXT NOT NULL,                -- RFC3339Nano
    reason        TEXT NOT NULL,
    score         REAL NOT NULL DEFAULT 0,      -- detector score that tripped suspend
    auto          INTEGER NOT NULL DEFAULT 1,   -- 1 = auto, 0 = manual admin
    reinstated_at TEXT,                          -- NULL while suspended
    reinstated_by TEXT                           -- "human:<actor>" once reinstated
);

CREATE INDEX IF NOT EXISTS idx_suspended_accounts_active
    ON suspended_accounts(account_id) WHERE reinstated_at IS NULL;

CREATE TABLE IF NOT EXISTS abuse_reports (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    report_id     TEXT NOT NULL UNIQUE,         -- random 16-byte hex
    received_at   TEXT NOT NULL,                -- RFC3339Nano
    source        TEXT NOT NULL,                -- reporter actor (email|"system"|"device:<id>")
    subject_id    TEXT NOT NULL,                -- account/object being reported
    category      TEXT NOT NULL,                -- phishing|spam|csam|fraud|harassment|malware|other
    description   TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '{}',   -- arbitrary JSON map
    status        TEXT NOT NULL DEFAULT 'open', -- open|investigating|actioned|dismissed
    handled_at    TEXT,                         -- RFC3339Nano of last status change
    handled_by    TEXT                          -- human actor of last status change
);

CREATE INDEX IF NOT EXISTS idx_abuse_reports_status_received
    ON abuse_reports(status, received_at);

CREATE INDEX IF NOT EXISTS idx_abuse_reports_subject
    ON abuse_reports(subject_id);

CREATE TABLE IF NOT EXISTS abuse_signals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT NOT NULL,                  -- RFC3339Nano
    kind        TEXT NOT NULL,                  -- signup|send|login|fanout|content|csam
    account_id  TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    score       REAL NOT NULL DEFAULT 0,
    reason      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_abuse_signals_ts ON abuse_signals(ts);
