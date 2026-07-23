-- 0001_trydemo_init.postgres.sql
-- Try-Vulos shared-demo accounting tables — Postgres variant.
-- BIGSERIAL for auto-increment, DOUBLE PRECISION for floating-point columns.

CREATE TABLE IF NOT EXISTS trydemo_ip_counters (
    ip                TEXT    NOT NULL,
    last_driver_claim TEXT,
    PRIMARY KEY (ip)
);

CREATE TABLE IF NOT EXISTS trydemo_month_accum (
    yyyymm              TEXT             PRIMARY KEY,
    compute_minutes     DOUBLE PRECISION NOT NULL DEFAULT 0,
    egress_bytes        BIGINT           NOT NULL DEFAULT 0,
    estimated_cost_usd  DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at          TEXT             NOT NULL
);

CREATE TABLE IF NOT EXISTS trydemo_session_events (
    id          BIGSERIAL PRIMARY KEY,
    event_type  TEXT    NOT NULL,
    ip          TEXT,
    session_id  TEXT,
    reason      TEXT,
    at          TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_trydemo_events_at ON trydemo_session_events (at);
