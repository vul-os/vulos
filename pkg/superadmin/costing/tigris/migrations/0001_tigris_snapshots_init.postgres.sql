-- 0001_tigris_snapshots_init.postgres.sql — Tigris cost snapshot table (Postgres).
CREATE TABLE IF NOT EXISTS tigris_snapshots (
    id               BIGSERIAL        PRIMARY KEY,
    taken_at         TEXT             NOT NULL,
    bucket           TEXT             NOT NULL DEFAULT '',
    storage_gb_hours DOUBLE PRECISION NOT NULL DEFAULT 0,
    egress_gb        DOUBLE PRECISION NOT NULL DEFAULT 0,
    request_count    BIGINT           NOT NULL DEFAULT 0,
    amount_usd       DOUBLE PRECISION NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_tigris_snap_taken_at ON tigris_snapshots(taken_at);
