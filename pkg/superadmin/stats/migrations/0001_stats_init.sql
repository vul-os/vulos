-- 0001_stats_init.sql — daily platform metrics snapshots.
--
-- Rows are upserted once per day at 02:00 UTC by the stats.Worker. The primary
-- key is the ISO-8601 date string (YYYY-MM-DD) so the time series is naturally
-- keyed and gap-filled by BackfillMissingDays.
--
-- BIGINT is used for numeric counters so that large account counts and MRR
-- (stored in ZAR cents) fit without truncation on both SQLite (BIGINT has
-- INTEGER affinity, 64-bit) and Postgres.

CREATE TABLE IF NOT EXISTS daily_snapshots (
    date                 TEXT    PRIMARY KEY,  -- YYYY-MM-DD
    total_accounts       BIGINT  NOT NULL DEFAULT 0,
    active_30d           BIGINT  NOT NULL DEFAULT 0,
    active_1d            BIGINT  NOT NULL DEFAULT 0,
    signups_today        BIGINT  NOT NULL DEFAULT 0,
    signups_by_tier_json TEXT    NOT NULL DEFAULT '{}',
    mrr_zar_cents        BIGINT  NOT NULL DEFAULT 0
);
