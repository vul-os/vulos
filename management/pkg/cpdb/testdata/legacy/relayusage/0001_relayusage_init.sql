-- 0001_relayusage_init.sql
-- Per-tenant relay usage metering (RELAY-USAGE-01).
-- Idempotent: safe to run against an existing DB.
--
-- relay_usage is the cp-side aggregate of bytes relayed + TURN/circuit sessions
-- reported by the stateless relay PoPs. Each PoP drains its in-memory meter
-- channel, aggregates per-tenant, and flushes periodically to
-- POST /api/relay/usage. The cp accumulates those flushes here, keyed by
-- (account_id, period) where period is the billing month "YYYY-MM".
--
-- Accumulation semantics: Add() upserts and SUMS bytes + sessions into the
-- existing row for the period (multiple PoPs report independently, and each PoP
-- flushes deltas on a timer — the cp is the authoritative sum across the
-- fleet). This mirrors the additive wallet-meter pattern; it is NOT idempotent
-- on a single report (re-sending a report double-counts), which is acceptable:
-- the relay's flush emits per-interval DELTAS, not cumulative totals.
CREATE TABLE IF NOT EXISTS relay_usage (
    account_id  TEXT NOT NULL,
    period      TEXT NOT NULL,            -- billing month "YYYY-MM" (UTC)
    bytes       INTEGER NOT NULL DEFAULT 0,
    sessions    INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL,            -- RFC3339 UTC of the last Add
    PRIMARY KEY (account_id, period)
);

CREATE INDEX IF NOT EXISTS ix_relay_usage_period ON relay_usage(period);
