-- 0002_relayusage_dedup.sql
-- Per-PoP report-id idempotency (RELAY-USAGE-01 hardening).
-- Idempotent: safe to run against an existing DB.
--
-- Relay usage reports are ADDITIVE deltas (relay_usage.bytes/sessions are
-- accumulated by Add). With no report identity a replayed POST /api/relay/usage
-- double-counts the billable usage. relay_usage_reports records the
-- (pop_id, report_id) of every accepted report so a replay is a no-op:
-- MarkReport does an INSERT and reports firstSeen=false on the UNIQUE conflict,
-- and the receiver skips the additive Add when the report was already seen.
--
-- report_id is the reporter-supplied nonce/sequence (monotonic per PoP). The
-- PRIMARY KEY (pop_id, report_id) makes the dedup per-PoP so two PoPs may reuse
-- the same low sequence numbers without colliding.
CREATE TABLE IF NOT EXISTS relay_usage_reports (
    pop_id     TEXT NOT NULL,
    report_id  TEXT NOT NULL,
    seen_at    TEXT NOT NULL,            -- RFC3339 UTC of first acceptance
    PRIMARY KEY (pop_id, report_id)
);

CREATE INDEX IF NOT EXISTS ix_relay_usage_reports_seen ON relay_usage_reports(seen_at);
