-- 0001_telemetry_init.sql
-- Root-cause telemetry catalog tables for the vulos.cloud control-plane.
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).
-- SUPPORT-01: per-tenant health signals with thresholding.

-- telemetry_samples stores one inbound sample per (ulid, sampled_at).
-- The OS-side agent pushes these; the control plane aggregates per-tenant.
CREATE TABLE IF NOT EXISTS telemetry_samples (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  ulid                  TEXT NOT NULL,           -- device ULID
  org_id                TEXT NOT NULL DEFAULT '',-- owning org (may be blank for personal tier)
  sampled_at            TEXT NOT NULL,           -- RFC3339Nano

  -- Network / reputation
  ip_reputation_score   INTEGER NOT NULL DEFAULT 100,  -- 0–100; < threshold = alert
  blocklist_spamhaus    INTEGER NOT NULL DEFAULT 0,    -- 1 = listed
  blocklist_barracuda   INTEGER NOT NULL DEFAULT 0,
  blocklist_sorbs       INTEGER NOT NULL DEFAULT 0,

  -- Storage
  disk_used_pct         INTEGER NOT NULL DEFAULT 0,    -- 0–100

  -- Queue depths
  sync_queue_depth      INTEGER NOT NULL DEFAULT 0,    -- pending sync items
  mail_queue_depth      INTEGER NOT NULL DEFAULT 0,    -- pending outbound mail msgs

  -- Mail health
  dkim_expiry_days      INTEGER NOT NULL DEFAULT 999,  -- days until DKIM key expires
  dmarc_pass_rate_pct   INTEGER NOT NULL DEFAULT 100,  -- 0–100 (rolling window)

  -- Instance
  uptime_sec            INTEGER NOT NULL DEFAULT 0,
  last_backup_age_sec   INTEGER NOT NULL DEFAULT 0     -- seconds since last successful backup
);

CREATE INDEX IF NOT EXISTS ix_telem_ulid_time  ON telemetry_samples(ulid, sampled_at DESC);
CREATE INDEX IF NOT EXISTS ix_telem_org_time   ON telemetry_samples(org_id, sampled_at DESC);

-- telemetry_alerts stores fired alerts (one row per firing event).
-- Consumers (SUPPORT-02 auto-remediation, SUPPORT-04 status page) poll this table.
CREATE TABLE IF NOT EXISTS telemetry_alerts (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ulid        TEXT NOT NULL,
  org_id      TEXT NOT NULL DEFAULT '',
  signal      TEXT NOT NULL,   -- signal name, e.g. "blocklist_spamhaus"
  severity    TEXT NOT NULL,   -- "warn" | "critical"
  detail      TEXT NOT NULL DEFAULT '',  -- human-readable detail
  fired_at    TEXT NOT NULL,
  resolved_at TEXT             -- NULL until resolved
);

CREATE INDEX IF NOT EXISTS ix_telem_alert_ulid ON telemetry_alerts(ulid, fired_at DESC);
CREATE INDEX IF NOT EXISTS ix_telem_alert_open ON telemetry_alerts(resolved_at) WHERE resolved_at IS NULL;
