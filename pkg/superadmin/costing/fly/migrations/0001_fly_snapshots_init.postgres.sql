-- 0001_fly_snapshots_init.postgres.sql — Fly cost snapshot table (Postgres).
CREATE TABLE IF NOT EXISTS fly_snapshots (
    id          BIGSERIAL PRIMARY KEY,
    taken_at    TEXT             NOT NULL,
    org_id      TEXT             NOT NULL DEFAULT '',
    app_name    TEXT             NOT NULL DEFAULT '',
    region      TEXT             NOT NULL DEFAULT '',
    amount_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,
    currency    TEXT             NOT NULL DEFAULT 'USD'
);
CREATE INDEX IF NOT EXISTS ix_fly_snap_taken_at ON fly_snapshots(taken_at);
