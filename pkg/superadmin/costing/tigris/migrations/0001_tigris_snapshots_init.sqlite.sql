-- 0001_tigris_snapshots_init.sqlite.sql — Tigris cost snapshot table (SQLite).
CREATE TABLE IF NOT EXISTS tigris_snapshots (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    taken_at         TEXT    NOT NULL,
    bucket           TEXT    NOT NULL DEFAULT '',
    storage_gb_hours REAL    NOT NULL DEFAULT 0,
    egress_gb        REAL    NOT NULL DEFAULT 0,
    request_count    INTEGER NOT NULL DEFAULT 0,
    amount_usd       REAL    NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_tigris_snap_taken_at ON tigris_snapshots(taken_at);
