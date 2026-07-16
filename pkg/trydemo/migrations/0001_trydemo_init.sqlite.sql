-- 0001_trydemo_init.sql
-- Try-Vulos shared-demo accounting tables.
-- Pure ephemeral state (no payload), idempotent.

CREATE TABLE IF NOT EXISTS trydemo_ip_counters (
    ip                TEXT    NOT NULL,
    last_driver_claim TEXT,                   -- RFC3339; for cooldown enforcement
    PRIMARY KEY (ip)
);

CREATE TABLE IF NOT EXISTS trydemo_month_accum (
    yyyymm           TEXT    PRIMARY KEY,     -- '202605'
    compute_minutes  REAL    NOT NULL DEFAULT 0,
    egress_bytes     INTEGER NOT NULL DEFAULT 0,
    estimated_cost_usd REAL  NOT NULL DEFAULT 0,
    updated_at       TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS trydemo_session_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type  TEXT    NOT NULL,  -- 'driver_claim' | 'driver_release' | 'driver_idle_kill' | 'machine_start' | 'machine_stop' | 'reset' | 'cap_engaged'
    ip          TEXT,
    session_id  TEXT,
    reason      TEXT,
    at          TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_trydemo_events_at ON trydemo_session_events (at);
