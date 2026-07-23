-- lancert: state for per-box LAN-hostname certificates issued via ACME DNS-01.
--
-- One row per registered box. The row carries:
--   * the most recently reported LAN IP (so the cloud can publish an A-record)
--   * the issued cert + key pair (PEM)
--   * the not-before / not-after timestamps (used by the renewal scheduler)
--   * the latest issuance log entries for diagnostics
--
-- SQLite variant: uses AUTOINCREMENT for lancert_issuance_log.id.

CREATE TABLE IF NOT EXISTS lancert_boxes (
    box_id            TEXT PRIMARY KEY,
    fqdn              TEXT NOT NULL,
    lan_ip            TEXT NOT NULL DEFAULT '',
    lan_ip_updated_at INTEGER NOT NULL DEFAULT 0,
    cert_pem          TEXT NOT NULL DEFAULT '',
    key_pem           TEXT NOT NULL DEFAULT '',
    not_before        INTEGER NOT NULL DEFAULT 0,
    not_after         INTEGER NOT NULL DEFAULT 0,
    last_issued_at    INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS lancert_issuance_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id      TEXT NOT NULL,
    fqdn        TEXT NOT NULL,
    event       TEXT NOT NULL,   -- 'issued' | 'renewed' | 'failed'
    detail      TEXT NOT NULL DEFAULT '',
    occurred_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_lancert_log_box ON lancert_issuance_log (box_id, occurred_at);
